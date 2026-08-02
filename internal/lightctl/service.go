// Package lightctl is the day-to-day Hue light-control feature — browsing
// rooms/lights, on/off/brightness/color, favorites, scenes. Deliberately
// separate from internal/engine, which has one specific job (own a DTLS
// screen-sync stream); conflating the two would make both harder to reason
// about for no benefit, since they share nothing except the underlying
// paired bridge.
package lightctl

import (
	"context"
	"sync"

	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/hue"
)

// Light is the UI-facing shape of a light — flattened and enriched (room
// name, favorite flag) compared to hue.Light's raw CLIP v2 fields.
type Light struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	RoomID     string  `json:"room_id"`
	RoomName   string  `json:"room_name"`
	On         bool    `json:"on"`
	Brightness float64 `json:"brightness"` // 0-100; meaningless if !Dimmable
	Dimmable   bool    `json:"dimmable"`
	Colorable  bool    `json:"colorable"`
	X          float64 `json:"x"` // CIE xy chromaticity; meaningless if !Colorable
	Y          float64 `json:"y"`
	Favorite   bool    `json:"favorite"`
}

// Room is a room or zone (both are CLIP v2 "grouped_light"-backed groups —
// see hue.Group) with its aggregate on/off/brightness state resolved.
// GroupedLightID (not Room.ID itself) is what room_toggle/room_brightness
// WS messages target — a room's own id names the group, its grouped_light
// service is the separate resource that actually accepts on/off/brightness.
type Room struct {
	ID             string  `json:"id"`
	GroupedLightID string  `json:"grouped_light_id"`
	Name           string  `json:"name"`
	On             bool    `json:"on"`
	Brightness     float64 `json:"brightness"`
	LightCount     int     `json:"light_count"`
	Favorite       bool    `json:"favorite"`
}

// SceneSwatch is one color in a scene's preview palette.
type SceneSwatch struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Scene is the UI-facing shape of a scene, including swatches for a color
// preview and a dynamic-scene indicator — the two things lights-ui's own
// (unused) scene code never had.
type Scene struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	GroupID     string        `json:"group_id"`
	GroupName   string        `json:"group_name"`
	AutoDynamic bool          `json:"auto_dynamic"`
	Speed       float64       `json:"speed"`
	Swatches    []SceneSwatch `json:"swatches"`
}

// LightEvent is a translated eventstream delta, ready to push to the
// browser as-is. Pointer fields mean "this field changed"; nil means
// "unchanged, not present in this partial update."
type LightEvent struct {
	Type       string   `json:"type"` // "light" | "grouped_light"
	ID         string   `json:"id"`
	On         *bool    `json:"on,omitempty"`
	Brightness *float64 `json:"brightness,omitempty"`
	X          *float64 `json:"x,omitempty"`
	Y          *float64 `json:"y,omitempty"`
}

// Service is the light-control feature's orchestrator: one per paired
// bridge, constructed alongside engine.Engine when pairing completes.
type Service struct {
	client    *hue.Client
	favorites *config.FavoritesStore

	mu           sync.RWMutex
	roomByDevice map[string]hue.Group // device rid -> owning room
	groupByID    map[string]hue.Group // room/zone id -> group (for scene group-name lookup)
}

// New builds a Service for an already-paired bridge.
func New(bridge config.Bridge, favorites *config.FavoritesStore) *Service {
	return &Service{
		client:    hue.NewClient(bridge.BridgeIP, bridge.Username, bridge.CertSHA256),
		favorites: favorites,
	}
}

// refreshGroups re-fetches rooms and zones fresh from the bridge — cheap,
// small payloads — so light/scene listings always reflect the current
// layout rather than a snapshot from whenever the app started (matching
// ROADMAP's "fetch the configuration fresh when the picker opens" rule for
// the screen-sync half of this app).
func (s *Service) refreshGroups(ctx context.Context) error {
	rooms, err := s.client.ListRooms(ctx)
	if err != nil {
		return err
	}
	zones, err := s.client.ListZones(ctx)
	if err != nil {
		return err
	}

	byDevice := make(map[string]hue.Group, 32)
	byID := make(map[string]hue.Group, len(rooms)+len(zones))
	for _, r := range rooms {
		byID[r.ID] = r
		for _, child := range r.Children {
			byDevice[child.RID] = r
		}
	}
	for _, z := range zones {
		byID[z.ID] = z
	}

	s.mu.Lock()
	s.roomByDevice = byDevice
	s.groupByID = byID
	s.mu.Unlock()
	return nil
}

// ListLights fetches every light, enriched with room name and favorite flag.
func (s *Service) ListLights(ctx context.Context) ([]Light, error) {
	if err := s.refreshGroups(ctx); err != nil {
		return nil, err
	}
	raw, err := s.client.ListLights(ctx)
	if err != nil {
		return nil, err
	}
	favs := s.favorites.All()

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Light, 0, len(raw))
	for _, l := range raw {
		room := s.roomByDevice[l.Owner.RID]
		_, fav := favs[l.ID]
		lt := Light{
			ID: l.ID, Name: l.Metadata.Name,
			RoomID: room.ID, RoomName: room.Metadata.Name,
			On: l.On.On, Favorite: fav,
		}
		if l.Dimming != nil {
			lt.Dimmable = true
			lt.Brightness = l.Dimming.Brightness
		}
		if l.Color != nil {
			lt.Colorable = true
			lt.X, lt.Y = l.Color.XY.X, l.Color.XY.Y
		}
		out = append(out, lt)
	}
	return out, nil
}

// ListRooms fetches every room with its aggregate state resolved.
func (s *Service) ListRooms(ctx context.Context) ([]Room, error) {
	rooms, err := s.client.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	favs := s.favorites.All()

	out := make([]Room, 0, len(rooms))
	for _, r := range rooms {
		room := Room{ID: r.ID, GroupedLightID: r.GroupedLightRID(), Name: r.Metadata.Name, LightCount: len(r.Children)}
		if gl, err := s.client.GetGroupedLight(ctx, r.GroupedLightRID()); err == nil {
			room.On = gl.On.On
			room.Brightness = gl.Dimming.Brightness
		}
		_, room.Favorite = favs["room:"+r.ID]
		out = append(out, room)
	}
	return out, nil
}

// ListScenes fetches every scene, with color-swatch previews and the
// dynamic-scene indicator.
func (s *Service) ListScenes(ctx context.Context) ([]Scene, error) {
	if err := s.refreshGroups(ctx); err != nil {
		return nil, err
	}
	raw, err := s.client.ListScenes(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Scene, 0, len(raw))
	for _, sc := range raw {
		group := s.groupByID[sc.Group.RID]
		swatches := make([]SceneSwatch, 0, len(sc.Palette.Color))
		for _, c := range sc.Palette.Color {
			swatches = append(swatches, SceneSwatch{X: c.Color.XY.X, Y: c.Color.XY.Y})
		}
		out = append(out, Scene{
			ID: sc.ID, Name: sc.Metadata.Name,
			GroupID: sc.Group.RID, GroupName: group.Metadata.Name,
			AutoDynamic: sc.AutoDynamic, Speed: sc.Speed,
			Swatches: swatches,
		})
	}
	return out, nil
}

// SetLightOn, SetLightBrightness and SetLightColorXY control one light.
func (s *Service) SetLightOn(ctx context.Context, rid string, on bool) error {
	return s.client.SetOn(ctx, rid, on)
}
func (s *Service) SetLightBrightness(ctx context.Context, rid string, pct float64) error {
	return s.client.SetBrightness(ctx, rid, pct)
}
func (s *Service) SetLightColorXY(ctx context.Context, rid string, x, y float64) error {
	return s.client.SetColorXY(ctx, rid, x, y)
}

// SetRoomOn and SetRoomBrightness control every light in a room/zone at once.
func (s *Service) SetRoomOn(ctx context.Context, groupedLightRID string, on bool) error {
	return s.client.SetGroupedLightOn(ctx, groupedLightRID, on)
}
func (s *Service) SetRoomBrightness(ctx context.Context, groupedLightRID string, pct float64) error {
	return s.client.SetGroupedLightBrightness(ctx, groupedLightRID, pct)
}

// RecallScene activates a scene.
func (s *Service) RecallScene(ctx context.Context, sceneRID string) error {
	return s.client.RecallScene(ctx, sceneRID)
}

// Identify blinks a light — reused for the same click-to-confirm UX the
// screen-sync calibration view already has.
func (s *Service) Identify(ctx context.Context, lightRID string) error {
	return s.client.Identify(ctx, lightRID)
}

// ToggleFavorite flips a light or room's favorite state (callers prefix
// room ids with "room:", matching lights-ui's existing convention) and
// returns the new state.
func (s *Service) ToggleFavorite(id string) bool {
	return s.favorites.Toggle(id)
}

// Subscribe wraps the bridge eventstream, translating raw partial resource
// updates into UI-ready LightEvents and filtering out everything that
// isn't a light or grouped_light change (e.g. geofence/presence noise).
func (s *Service) Subscribe(ctx context.Context) <-chan LightEvent {
	out := make(chan LightEvent)
	go func() {
		defer close(out)
		for ev := range s.client.SubscribeEvents(ctx) {
			for _, d := range ev.Data {
				le, ok := translateUpdate(d)
				if !ok {
					continue
				}
				select {
				case out <- le:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func translateUpdate(d map[string]any) (LightEvent, bool) {
	typ, _ := d["type"].(string)
	id, _ := d["id"].(string)
	if id == "" || (typ != "light" && typ != "grouped_light") {
		return LightEvent{}, false
	}
	le := LightEvent{Type: typ, ID: id}
	if onRaw, ok := d["on"].(map[string]any); ok {
		if v, ok := onRaw["on"].(bool); ok {
			le.On = &v
		}
	}
	if dimRaw, ok := d["dimming"].(map[string]any); ok {
		if v, ok := dimRaw["brightness"].(float64); ok {
			le.Brightness = &v
		}
	}
	if colorRaw, ok := d["color"].(map[string]any); ok {
		if xyRaw, ok := colorRaw["xy"].(map[string]any); ok {
			if v, ok := xyRaw["x"].(float64); ok {
				le.X = &v
			}
			if v, ok := xyRaw["y"].(float64); ok {
				le.Y = &v
			}
		}
	}
	return le, true
}
