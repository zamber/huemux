// Package engine wires the bridge client, zone mapping, color pipeline and
// smoother together and owns the output loop. It is the runtime core that
// both the CLI (cmd/huemux) and the localhost server (internal/server)
// drive.
package engine

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/pipeline"
)

// ZoneStatus is a zone plus the color currently being sent for it — enough
// for the calibration UI to draw outlines filled with live color without a
// second round trip.
type ZoneStatus struct {
	pipeline.Zone
	R uint8 `json:"R"`
	G uint8 `json:"G"`
	B uint8 `json:"B"`
}

// Status is the full snapshot pushed to the browser once a second (and on
// change) and printed by the CLI status readout.
type Status struct {
	BridgeIP        string `json:"BridgeIP"`
	BridgeConnected bool   `json:"BridgeConnected"`
	HandshakeMS     int64  `json:"HandshakeMS"`

	AreaID       string `json:"AreaID"`
	AreaName     string `json:"AreaName"`
	AreaType     string `json:"AreaType"`
	ChannelCount int    `json:"ChannelCount"`
	AreaBusyBy   string `json:"AreaBusyBy"` // non-empty if another app holds the stream

	StreamActive bool   `json:"StreamActive"`
	OutputHz     int    `json:"OutputHz"`
	Sent         uint64 `json:"Sent"`
	LastError    string `json:"LastError"`

	CaptureClients int     `json:"CaptureClients"`
	InboundFPS     float64 `json:"InboundFPS"`
	CaptureW       int     `json:"CaptureW"`
	CaptureH       int     `json:"CaptureH"`
	GridW          int     `json:"GridW"`
	GridH          int     `json:"GridH"`

	Settings config.AreaSettings `json:"Settings"`
	Zones    []ZoneStatus        `json:"Zones"`
}

// Engine owns exactly one active area's worth of state at a time — a
// deliberate v1 simplification (see ROADMAP non-goals: no simultaneous
// areas).
type Engine struct {
	client *hue.Client
	store  *config.Store
	bridge config.Bridge

	selectMu     sync.Mutex // serializes SelectArea/Stop so concurrent select_area messages cannot orphan a stream
	mu           sync.Mutex
	areaCfg      hue.EntertainmentConfiguration
	zones        []pipeline.Zone
	lightFeature map[string]hue.LightColorFeatures // by light rid
	settings     config.AreaSettings
	grid         *pipeline.Grid
	mask         pipeline.LetterboxMask
	lastFrameAt  time.Time
	frameCount   int
	fpsWindow    time.Time
	inboundFPS   float64
	captureW     int
	captureH     int
	uiClients    int
	handshakeMS  int64
	busyBy       string
	lastColors   map[uint8][3]byte // per-channel color from the most recent tick, for the status snapshot

	stream   *hue.Stream
	smoother *pipeline.Smoother
	cancel   context.CancelFunc
	loopDone chan struct{}
}

// New builds an Engine for an already-paired bridge.
func New(bridge config.Bridge, store *config.Store) *Engine {
	return &Engine{
		client:       hue.NewClient(bridge.BridgeIP, bridge.Username),
		store:        store,
		bridge:       bridge,
		lightFeature: map[string]hue.LightColorFeatures{},
		smoother:     pipeline.NewSmoother(),
	}
}

// ListAreas fetches every entertainment area, fresh from the bridge.
func (e *Engine) ListAreas(ctx context.Context) ([]hue.EntertainmentConfiguration, error) {
	return e.client.ListEntertainmentConfigurations(ctx)
}

// SelectArea stops any existing stream, fetches the requested area fresh
// (users rearrange areas in the Hue app and expect that to show up), builds
// its zones, resolves each zone's light for click-to-identify, and starts
// streaming.
func (e *Engine) SelectArea(ctx context.Context, areaID string) error {
	e.selectMu.Lock()
	defer e.selectMu.Unlock()
	e.stopLocked(ctx)

	cfg, err := e.client.GetEntertainmentConfiguration(ctx, areaID)
	if err != nil {
		return fmt.Errorf("fetch entertainment configuration: %w", err)
	}
	if cfg.IsActive() {
		e.mu.Lock()
		e.busyBy = cfg.ActiveStreamer.RID
		e.mu.Unlock()
		return hue.ErrAreaBusy
	}

	settings := e.store.Get(areaID, cfg.ConfigurationType)
	zones := pipeline.BuildZones(cfg.Channels, zoneOptsFromSettings(settings))

	features := map[string]hue.LightColorFeatures{}
	for i, z := range zones {
		if z.LightRID == "" {
			continue
		}
		lightRID, err := e.client.ResolveLightForService(ctx, z.LightRID)
		if err != nil {
			log.Printf("huemux: resolve light for channel %d: %v", z.ChannelID, err)
			continue
		}
		zones[i].LightRID = lightRID
		if _, ok := features[lightRID]; !ok {
			if f, err := e.client.GetLightFeatures(ctx, lightRID); err == nil {
				features[lightRID] = f
			}
			// Entertainment streaming drives color/brightness only, never
			// on/off — a physically-off bulb would sit dark through a
			// perfectly working stream with nothing anywhere to explain why.
			if err := e.client.SetOn(ctx, lightRID, true); err != nil {
				log.Printf("huemux: turn on light %s: %v", lightRID, err)
			}
		}
	}

	if err := e.client.StartStreaming(ctx, areaID); err != nil {
		return fmt.Errorf("start streaming: %w", err)
	}

	start := time.Now()
	stream, err := hue.Dial(ctx, hue.Config{
		BridgeIP:   e.bridge.BridgeIP,
		Username:   e.client.Username,
		ClientKey:  e.bridge.ClientKey,
		AreaID:     areaID,
		ColorSpace: colorSpaceFromString(settings.ColorSpace),
		OutputHz:   settings.OutputHz,
		DisableEMS: settings.DisableEMS,
	})
	if err != nil {
		_ = e.client.StopStreaming(ctx, areaID)
		return fmt.Errorf("dial dtls stream: %w", err)
	}
	handshakeMS := time.Since(start).Milliseconds()

	e.mu.Lock()
	e.areaCfg = cfg
	e.zones = zones
	e.lightFeature = features
	e.settings = settings
	e.stream = stream
	e.handshakeMS = handshakeMS
	e.busyBy = ""
	e.grid = nil
	e.lastColors = nil
	e.mu.Unlock()
	e.smoother.Reset()

	runCtx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.loopDone = make(chan struct{})

	go func() {
		if err := stream.Run(runCtx); err != nil && runCtx.Err() == nil {
			log.Printf("huemux: stream ended: %v", err)
		}
	}()
	go e.outputLoop(runCtx)

	return nil
}

func colorSpaceFromString(s string) hue.ColorSpace {
	if s == "xy" {
		return hue.ColorSpaceXY
	}
	return hue.ColorSpaceRGB
}

func zoneOptsFromSettings(s config.AreaSettings) pipeline.ZoneOpts {
	return pipeline.ZoneOpts{
		Mode:             pipeline.SampleMode(s.Mode),
		EdgeWidth:        s.EdgeWidth,
		QuadrantSize:     s.QuadrantSize,
		AxisHorizontal:   pipeline.Axis(s.AxisHorizontal),
		AxisVertical:     pipeline.Axis(s.AxisVertical),
		AxisDepth:        pipeline.Axis(s.AxisDepth),
		InvertHorizontal: s.InvertHorizontal,
		InvertVertical:   s.InvertVertical,
		InvertDepth:      s.InvertDepth,
		DepthSizeGain:    s.DepthSizeGain,
		Feather:          s.Feather,
	}
}

// Stop ends the current stream cleanly (fade to black, happens inside
// Stream.Run's shutdown path) and releases the area.
func (e *Engine) Stop(ctx context.Context) {
	e.selectMu.Lock()
	defer e.selectMu.Unlock()
	e.stopLocked(ctx)
}

// stopLocked is the shared Stop body; it must only be called with selectMu
// held (Stop and SelectArea both serialize on it).
func (e *Engine) stopLocked(ctx context.Context) {
	e.mu.Lock()
	cancel := e.cancel
	stream := e.stream
	areaID := e.areaCfg.ID
	done := e.loopDone
	e.cancel = nil
	e.stream = nil
	e.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
	if stream != nil {
		_ = stream.Close()
	}
	if areaID != "" {
		_ = e.client.StopStreaming(ctx, areaID)
	}
}

// SetFrame is called by the server layer with every grid received from the
// browser tab designated as the frame source. It only updates state; the
// output loop, on its own ticker, is what decides when to act on it.
func (e *Engine) SetFrame(g *pipeline.Grid) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.grid = g
	e.mask = pipeline.DetectLetterbox(g)
	e.captureW, e.captureH = g.W, g.H
	e.lastFrameAt = time.Now()
	e.frameCount++
	if e.fpsWindow.IsZero() {
		e.fpsWindow = e.lastFrameAt
	}
	if elapsed := e.lastFrameAt.Sub(e.fpsWindow); elapsed >= time.Second {
		e.inboundFPS = float64(e.frameCount) / elapsed.Seconds()
		e.frameCount = 0
		e.fpsWindow = e.lastFrameAt
	}
}

// UpdateSettings merges new settings for the active area, persists them
// (debounced), and applies whatever needs re-deriving (zone geometry if
// sampling params changed).
func (e *Engine) UpdateSettings(s config.AreaSettings) {
	e.mu.Lock()
	areaID := e.areaCfg.ID
	e.settings = s
	channels := e.areaCfg.Channels
	e.mu.Unlock()

	if areaID == "" {
		return
	}
	zones := pipeline.BuildZones(channels, zoneOptsFromSettings(s))
	e.mu.Lock()
	// Preserve resolved light rids from the original zone build (BuildZones
	// re-derives geometry but has no bridge access to re-resolve services).
	byChannel := map[uint8]string{}
	for _, z := range e.zones {
		byChannel[z.ChannelID] = z.LightRID
	}
	for i := range zones {
		if rid, ok := byChannel[zones[i].ChannelID]; ok {
			zones[i].LightRID = rid
		}
	}
	e.zones = zones
	e.mu.Unlock()

	e.store.Set(areaID, s)
}

// Identify blinks the physical light behind a zone's channel.
func (e *Engine) Identify(ctx context.Context, lightRID string) error {
	return e.client.Identify(ctx, lightRID)
}

// IncUIClient / DecUIClient track how many browser tabs are connected for
// status purposes, independent of which one (if any) is the frame source.
func (e *Engine) IncUIClient() { e.mu.Lock(); e.uiClients++; e.mu.Unlock() }
func (e *Engine) DecUIClient() { e.mu.Lock(); e.uiClients--; e.mu.Unlock() }

// outputLoop is the fixed-rate clock the roadmap insists on: it runs
// whether or not new capture frames have arrived, sampling whatever the
// current grid is, and it is the only place color values are computed and
// handed to the Stream.
func (e *Engine) outputLoop(ctx context.Context) {
	defer close(e.loopDone)

	e.mu.Lock()
	hz := e.settings.OutputHz
	e.mu.Unlock()
	if hz <= 0 {
		hz = 20
	}
	t := time.NewTicker(time.Second / time.Duration(hz))
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			e.tick(now)
		}
	}
}

func (e *Engine) tick(now time.Time) {
	e.mu.Lock()
	grid := e.grid
	mask := e.mask
	zones := append([]pipeline.Zone(nil), e.zones...)
	settings := e.settings
	features := e.lightFeature
	stream := e.stream
	e.mu.Unlock()

	if stream == nil || len(zones) == 0 {
		return
	}

	raw := make(map[uint8]pipeline.LinearColor, len(zones))
	for _, z := range zones {
		r, g, b := pipeline.SampleZoneLinear(grid, mask, z)
		raw[z.ChannelID] = pipeline.LinearColor{R: r, G: g, B: b}
	}

	smoothed := e.smoother.Step(raw, now, settings.Reactivity)

	channels := make([]hue.Channel, 0, len(zones))
	colorsByID := make(map[uint8][3]byte, len(zones))
	for _, z := range zones {
		c := smoothed[z.ChannelID]
		gain := 1.0
		if g, ok := settings.ChannelBrightness[z.ChannelID]; ok {
			gain = g
		}
		colorCapable := true
		if f, ok := features[z.LightRID]; ok {
			colorCapable = f.SupportsColor
		}
		params := pipeline.ColorParams{
			Saturation:   settings.Saturation,
			Brightness:   settings.Brightness,
			BlackCutoff:  settings.BlackCutoff,
			ChannelGain:  gain,
			ColorSpace:   colorSpaceFromString(settings.ColorSpace),
			ColorCapable: colorCapable,
		}
		ch := pipeline.Process(c.R, c.G, c.B, z.ChannelID, params)
		channels = append(channels, ch)
		// Always real RGB for the status/preview snapshot, independent of
		// which color space is actually on the wire — ch.R/G/B are x/y/
		// brightness bytes, not displayable, whenever ColorSpaceXY is
		// selected (see pipeline.DisplayRGB).
		colorsByID[z.ChannelID] = pipeline.DisplayRGB(c.R, c.G, c.B, params)
	}
	stream.Set(channels)

	// Cache this tick's colors so Snapshot() — polled once a second by the
	// status push and by GET /api/status — has something to show. Without
	// this the calibration preview would always render every zone black,
	// even while the bridge is receiving a perfectly correct color stream.
	e.mu.Lock()
	e.lastColors = colorsByID
	e.mu.Unlock()
}

func (e *Engine) snapshotLocked(zones []pipeline.Zone, colors map[uint8][3]byte) Status {
	e.mu.Lock()
	defer e.mu.Unlock()

	zs := make([]ZoneStatus, 0, len(zones))
	for _, z := range zones {
		c := colors[z.ChannelID]
		zs = append(zs, ZoneStatus{Zone: z, R: c[0], G: c[1], B: c[2]})
	}

	var sent uint64
	var lastErr string
	if e.stream != nil {
		sent = e.stream.Sent
		if e.stream.LastError != nil {
			lastErr = e.stream.LastError.Error()
		}
	}

	return Status{
		BridgeIP:        e.bridge.BridgeIP,
		BridgeConnected: e.stream != nil,
		HandshakeMS:     e.handshakeMS,
		AreaID:          e.areaCfg.ID,
		AreaName:        e.areaCfg.Metadata.Name,
		AreaType:        e.areaCfg.ConfigurationType,
		ChannelCount:    len(e.areaCfg.Channels),
		AreaBusyBy:      e.busyBy,
		StreamActive:    e.stream != nil,
		OutputHz:        e.settings.OutputHz,
		Sent:            sent,
		LastError:       lastErr,
		CaptureClients:  e.uiClients,
		InboundFPS:      e.inboundFPS,
		CaptureW:        e.captureW,
		CaptureH:        e.captureH,
		GridW:           gridW(e.grid),
		GridH:           gridH(e.grid),
		Settings:        e.settings,
		Zones:           zs,
	}
}

// Snapshot returns the current status without waiting for the next tick —
// used for GET /api/status and the initial push to a newly connected tab.
func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	zones := append([]pipeline.Zone(nil), e.zones...)
	colors := e.lastColors
	e.mu.Unlock()
	return e.snapshotLocked(zones, colors)
}

func gridW(g *pipeline.Grid) int {
	if g == nil {
		return 0
	}
	return g.W
}

func gridH(g *pipeline.Grid) int {
	if g == nil {
		return 0
	}
	return g.H
}
