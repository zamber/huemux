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
	"github.com/zamber/huemux/internal/debuglog"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/music"
	"github.com/zamber/huemux/internal/pipeline"
	"github.com/zamber/huemux/internal/preset"
)

// CaptureMode selects which input source the output loop consumes.
// It is a post-capture routing decision: on Android the single MediaProjection
// session always captures both video and audio when available; this decides
// which one the engine acts on.
type CaptureMode string

const (
	CaptureVideo      CaptureMode = "video"
	CaptureAudio      CaptureMode = "audio"
	CaptureAudioVideo CaptureMode = "audiovideo"
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

	// MusicPreset is the active music-reactivity preset slug; empty while
	// screen sync drives the output.
	MusicPreset string `json:"MusicPreset"`

	// CaptureMode is the active input routing: video, audio, or audiovideo.
	CaptureMode string `json:"CaptureMode"`

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
	busyAreaID   string            // area another app holds, remembered so a stop can release it on the bridge
	lastColors   map[uint8][3]byte // per-channel color from the most recent tick, for the status snapshot

	stream   *hue.Stream
	smoother *pipeline.Smoother
	cancel   context.CancelFunc
	loopDone chan struct{}

	// Music reactivity. The runner is non-nil while a preset drives the
	// output; the frame source is wired once by the server (the audio state
	// lives there, the output clock here). musicPreset is the active preset
	// slug — re-activation after an area switch needs the slug, not the
	// display name. All guarded by mu like the rest.
	musicRunner *preset.Runner
	musicFrame  func() (music.Frame, bool)
	musicPreset string

	// captureMode selects which input source drives the output loop.
	// Defaults to CaptureVideo (backwards-compatible). Set by the server
	// on capture_mode control messages. Guarded by mu.
	captureMode CaptureMode

	// tickDiagLogged is set after the first tick diagnostic is emitted,
	// so the "no source active" warning fires once per session rather
	// than on every tick.
	tickDiagLogged bool
}

// New builds an Engine for an already-paired bridge.
func New(bridge config.Bridge, store *config.Store) *Engine {
	return &Engine{
		client:       hue.NewClient(bridge.BridgeIP, bridge.Username, bridge.CertSHA256),
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
		// Remember which area is busy: the local instance did not get to
		// stream, so a later "stop" has to release *this* area on the
		// bridge — that is the only way the user can take over from
		// whichever other instance is holding it.
		e.mu.Lock()
		e.busyBy = cfg.ActiveStreamer.RID
		e.busyAreaID = cfg.ID
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
	e.busyAreaID = ""
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
	busyAreaID := e.busyAreaID
	done := e.loopDone
	e.cancel = nil
	e.stream = nil
	e.mu.Unlock()

	if cancel == nil && busyAreaID == "" {
		return
	}
	// cancel is nil exactly when there is no local stream to end (the
	// busy-release path below) — calling a nil func value panics, and the
	// WS handler does not recover from it.
	if cancel != nil {
		cancel()
		if done != nil {
			<-done
		}
	}
	if stream != nil {
		_ = stream.Close()
	}
	if areaID != "" {
		_ = e.client.StopStreaming(ctx, areaID)
	} else if busyAreaID != "" {
		// No local stream to end — but the area the user tried to select
		// is held by another instance, and this stop is the only way to
		// prise it loose. The bridge accepts {"action":"stop"} from any
		// authenticated client and releases the active streamer.
		log.Printf("huemux: releasing busy area %s on the bridge", busyAreaID)
		if err := e.client.StopStreaming(ctx, busyAreaID); err != nil {
			log.Printf("huemux: release busy area %s: %v", busyAreaID, err)
		}
	}
	e.mu.Lock()
	if e.busyAreaID == busyAreaID && busyAreaID != "" {
		e.busyAreaID = ""
		e.busyBy = ""
	}
	e.mu.Unlock()
}

// SetMusicFrameSource wires the audio analysis state reader. The music
// capture lives in the server layer, the output clock here; called once at
// pairing time (or at construction, when an engine already exists). A nil fn
// is fine — presets then run on silence.
func (e *Engine) SetMusicFrameSource(fn func() (music.Frame, bool)) {
	e.mu.Lock()
	e.musicFrame = fn
	e.mu.Unlock()
}

// SetCaptureMode selects which input source drives the output loop.
// Safe to call while the stream is running — takes effect on the next tick.
func (e *Engine) SetCaptureMode(mode CaptureMode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mode != CaptureVideo && mode != CaptureAudio && mode != CaptureAudioVideo {
		return // ignore unknown modes
	}
	e.captureMode = mode
}

// CaptureMode reports the active input routing mode.
func (e *Engine) CaptureMode() CaptureMode {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.captureMode == "" {
		return CaptureVideo // default
	}
	return e.captureMode
}

// ActivateMusic starts driving the output loop from the named built-in
// preset; pass "" to hand control back to screen sync. channels maps light
// RIDs to area channel ids, positions their room coordinates — both derived
// from the current area's zone layout by the caller.
func (e *Engine) ActivateMusic(slug string, channels map[string]uint8, positions map[string]preset.Pos3) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if slug == "" {
		e.musicRunner = nil
		e.musicPreset = ""
		return nil
	}
	p, err := preset.Builtin(slug)
	if err != nil {
		return err
	}
	e.musicRunner = preset.NewRunner(p, channels, positions, e.musicFrame)
	e.musicPreset = slug
	return nil
}

// MusicPreset returns the active music preset slug, or "" while screen sync
// drives the output.
func (e *Engine) MusicPreset() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.musicPreset
}

// MusicLayout derives the light→channel and light→position maps a preset
// runner needs, from the current area's zone layout. Returns nil maps when
// no area is selected — activation then still succeeds, but paints nothing
// until one is.
func (e *Engine) MusicLayout() (channels map[string]uint8, positions map[string]preset.Pos3) {
	e.mu.Lock()
	defer e.mu.Unlock()
	posByChannel := make(map[uint8]hue.Position, len(e.areaCfg.Channels))
	for _, ch := range e.areaCfg.Channels {
		posByChannel[ch.ChannelID] = ch.Position
	}
	channels = make(map[string]uint8, len(e.zones))
	positions = make(map[string]preset.Pos3, len(e.zones))
	for _, z := range e.zones {
		if z.LightRID == "" {
			continue
		}
		channels[z.LightRID] = z.ChannelID
		if p, ok := posByChannel[z.ChannelID]; ok {
			positions[z.LightRID] = preset.Pos3{X: p.X, Y: p.Y, Z: p.Z}
		}
	}
	if len(channels) == 0 {
		return nil, nil
	}
	return channels, positions
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
	if hz < 1 || hz > 25 {
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
	musicRunner := e.musicRunner
	captureMode := e.captureMode
	if captureMode == "" {
		captureMode = CaptureVideo // default
	}
	e.mu.Unlock()

	if stream == nil {
		return
	}

	// Diagnostic: once per session, record what is driving the output.
	// On Android this is the only way to know whether audio capture reached
	// the engine or silently stalled upstream.
	if !e.tickDiagLogged {
		e.tickDiagLogged = true
		e.mu.Lock()
		mode := e.captureMode
		hasGrid := e.grid != nil
		hasRunner := e.musicRunner != nil
		e.mu.Unlock()
		if mode == "" {
			mode = CaptureVideo
		}
		debuglog.Audiof("tick: mode=%s grid=%v preset=%v zones=%d", mode, hasGrid, hasRunner, len(zones))
	}

	// Two input paths share one output clock (DP-8): an active music preset
	// replaces the grid as the color source (screen sync is "paused"; a
	// blend mode is Phase 4). Zones are still what names the channels, so
	// the per-channel processing below is unchanged either way.
	//
	// CaptureMode decides which source wins when both are available:
	//   video       — grid only (backwards-compatible default)
	//   audio       — music preset only; grid ignored
	//   audiovideo  — music preset if active, otherwise grid
	var raw map[uint8]pipeline.LinearColor
	switch captureMode {
	case CaptureAudio:
		if musicRunner != nil {
			raw = musicRunner.Step(now)
		} else {
			// Audio mode with no preset: send a low-flux neutral frame
			// so the DTLS session does not time out while waiting for
			// the first audio frames to arrive.
			raw = e.neutralFrameLocked(zones)
		}
	case CaptureAudioVideo:
		if musicRunner != nil {
			raw = musicRunner.Step(now)
		} else if grid != nil {
			raw = e.sampleGridLocked(grid, mask, zones)
		} else {
			raw = e.neutralFrameLocked(zones)
		}
	default: // CaptureVideo or empty
		if grid == nil {
			raw = e.neutralFrameLocked(zones)
		} else {
			raw = e.sampleGridLocked(grid, mask, zones)
		}
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

// sampleGridLocked samples every zone from the current grid. Extracted from
// tick() so the three capture-mode branches share one implementation.
func (e *Engine) sampleGridLocked(grid *pipeline.Grid, mask pipeline.LetterboxMask, zones []pipeline.Zone) map[uint8]pipeline.LinearColor {
	if len(zones) == 0 || grid == nil {
		return nil
	}
	raw := make(map[uint8]pipeline.LinearColor, len(zones))
	for _, z := range zones {
		r, g, b := pipeline.SampleZoneLinear(grid, mask, z)
		raw[z.ChannelID] = pipeline.LinearColor{R: r, G: g, B: b}
	}
	return raw
}

// neutralFrameLocked returns a dim neutral frame for every zone — enough to
// keep the DTLS session alive when no real source is driving the output
// (e.g. audio mode before the first audio frames arrive). Brightness floor
// of ~1% is below the human-noticeable threshold on most bulbs but satisfies
// the bridge's keepalive requirement.
func (e *Engine) neutralFrameLocked(zones []pipeline.Zone) map[uint8]pipeline.LinearColor {
	if len(zones) == 0 {
		return nil
	}
	raw := make(map[uint8]pipeline.LinearColor, len(zones))
	for _, z := range zones {
		raw[z.ChannelID] = pipeline.LinearColor{R: 0.005, G: 0.005, B: 0.005}
	}
	return raw
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
		var streamErr error
		sent, streamErr = e.stream.Stats()
		if streamErr != nil {
			lastErr = streamErr.Error()
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
		MusicPreset:     e.musicPreset,
		CaptureMode:     string(e.captureMode),
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
