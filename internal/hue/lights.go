package hue

import (
	"context"
	"fmt"
	"net/http"
)

// Light is the subset of the CLIP v2 `light` resource the control panel
// needs. Capability fields (Dimming/ColorTemperature/Color) are pointers:
// CLIP v2 omits the key entirely for a light that lacks that capability
// (a white-only bulb has no "color" key at all), so nil here means
// "this light can't do that," not "value happens to be zero."
type Light struct {
	ID       string             `json:"id"`
	Owner    ResourceIdentifier `json:"owner"` // the light's parent device
	Metadata Metadata           `json:"metadata"`
	On       struct {
		On bool `json:"on"`
	} `json:"on"`
	Dimming *struct {
		Brightness float64 `json:"brightness"` // 0-100
	} `json:"dimming,omitempty"`
	ColorTemperature *struct {
		Mirek int `json:"mirek"`
	} `json:"color_temperature,omitempty"`
	Color *struct {
		XY struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"xy"`
	} `json:"color,omitempty"`
}

// Group is the shape shared by CLIP v2's `room` and `zone` resources: a
// named collection of children with a `grouped_light` service for
// aggregate on/off/brightness control. The one real difference — a room's
// children are devices, a zone's are lights directly — doesn't affect any
// field lightctl needs, so one Go type covers both; ListRooms/ListZones
// just hit different endpoints.
//
// Not to be confused with pipeline.Zone (a screen-sampling rectangle) in
// the screen-sync half of this app — same word, unrelated Hue vs.
// image-processing concepts, different packages.
type Group struct {
	ID       string               `json:"id"`
	Children []ResourceIdentifier `json:"children"`
	Services []ResourceIdentifier `json:"services"`
	Metadata Metadata             `json:"metadata"`
}

// GroupedLightRID returns the rid of this group's aggregate grouped_light
// service, or "" if (unexpectedly) absent.
func (g Group) GroupedLightRID() string {
	for _, s := range g.Services {
		if s.RType == "grouped_light" {
			return s.RID
		}
	}
	return ""
}

// Device is the CLIP v2 `device` resource — needed only to resolve a
// light's owner (a device rid) to the room that lists that device among
// its children, since rooms group by device rather than by light directly.
type Device struct {
	ID       string   `json:"id"`
	Metadata Metadata `json:"metadata"`
}

// GroupedLight is CLIP v2's aggregate control resource for a room or zone:
// PUT to it once instead of iterating every member light individually.
type GroupedLight struct {
	ID      string            `json:"id"`
	On      struct{ On bool } `json:"on"`
	Dimming struct {
		Brightness float64 `json:"brightness"`
	} `json:"dimming"`
}

// ScenePaletteColor is one swatch in a scene's curated preview palette —
// Hue's own app uses exactly this field for scene swatches, so there's no
// need to derive a preview by hand from individual light actions.
type ScenePaletteColor struct {
	Color struct {
		XY struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"xy"`
	} `json:"color"`
}

// Scene is the CLIP v2 `scene` resource. AutoDynamic and Speed are real,
// confirmed-live fields (checked against the actual bridge, not assumed):
// AutoDynamic true means the scene cycles through its palette over time
// rather than holding one static look; Speed (0..1) is how fast.
type Scene struct {
	ID       string             `json:"id"`
	Metadata Metadata           `json:"metadata"`
	Group    ResourceIdentifier `json:"group"` // the room/zone this scene belongs to
	Palette  struct {
		Color []ScenePaletteColor `json:"color"`
	} `json:"palette"`
	Speed       float64 `json:"speed"`
	AutoDynamic bool    `json:"auto_dynamic"`
	Status      struct {
		Active string `json:"active"` // "active" | "inactive" | "static" | "dynamic_palette"
	} `json:"status"`
}

// ListLights fetches every light known to the bridge.
func (c *Client) ListLights(ctx context.Context) ([]Light, error) {
	var env v2Envelope[Light]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/light", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// ListRooms fetches every room (device-grouped, matches the layout in the
// Hue app's room list).
func (c *Client) ListRooms(ctx context.Context) ([]Group, error) {
	var env v2Envelope[Group]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/room", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// ListZones fetches every zone (light-grouped — a cross-room grouping like
// "all garden lights").
func (c *Client) ListZones(ctx context.Context) ([]Group, error) {
	var env v2Envelope[Group]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/zone", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// ListDevices fetches every device, used to resolve a light's owner to the
// room/zone that lists that device as a child.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var env v2Envelope[Device]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/device", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// SetBrightness sets a light's dimming level, 0-100.
func (c *Client) SetBrightness(ctx context.Context, lightRID string, pct float64) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/light/"+lightRID,
		map[string]any{"dimming": map[string]any{"brightness": pct}}, nil)
}

// SetColorXY sets a light's color via CIE xy chromaticity — CLIP v2 has no
// direct hue/saturation field; a picker built on hue/saturation or RGB must
// convert before calling this (see internal/pipeline/color.go's
// linear-RGB-to-xy math, reused rather than reimplemented for the
// light-control panel's color picker).
func (c *Client) SetColorXY(ctx context.Context, lightRID string, x, y float64) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/light/"+lightRID,
		map[string]any{"color": map[string]any{"xy": map[string]any{"x": x, "y": y}}}, nil)
}

// GetGroupedLight fetches one room/zone's aggregate state.
func (c *Client) GetGroupedLight(ctx context.Context, rid string) (GroupedLight, error) {
	var env v2Envelope[GroupedLight]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/grouped_light/"+rid, nil, &env); err != nil {
		return GroupedLight{}, err
	}
	if len(env.Data) == 0 {
		return GroupedLight{}, fmt.Errorf("no grouped_light with id %s", rid)
	}
	return env.Data[0], nil
}

// SetGroupedLightOn turns every light in a room/zone on or off with one PUT.
func (c *Client) SetGroupedLightOn(ctx context.Context, rid string, on bool) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/grouped_light/"+rid,
		map[string]any{"on": map[string]any{"on": on}}, nil)
}

// SetGroupedLightBrightness sets every light in a room/zone to the same
// dimming level with one PUT.
func (c *Client) SetGroupedLightBrightness(ctx context.Context, rid string, pct float64) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/grouped_light/"+rid,
		map[string]any{"dimming": map[string]any{"brightness": pct}}, nil)
}

// ListScenes fetches every scene known to the bridge.
func (c *Client) ListScenes(ctx context.Context) ([]Scene, error) {
	var env v2Envelope[Scene]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/scene", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// RecallScene activates a scene.
func (c *Client) RecallScene(ctx context.Context, sceneRID string) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/scene/"+sceneRID,
		map[string]any{"recall": map[string]any{"action": "active"}}, nil)
}
