package server

// Color-temperature control and payload tests — the backend half of the
// white-temperature picker. The frontend sends light_color_temp over the WS
// control channel; the server must forward a color_temperature PUT to the
// bridge, and /api/lights + /api/rooms must carry the fields the picker and
// room icons need (mirek, ct_capable, archetype).

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/lightctl"
)

// testCTLight is testLight plus a color_temperature payload — a white-ambiance
// bulb (CT only, no Color) unless withColor is set, in which case it stands in
// for a full-color bulb that also reports its current mirek.
func testCTLight(id, name, device string, mirek int, withColor bool) hue.Light {
	l := testLight(id, name, device, true, 80)
	l.ColorTemperature = &struct {
		Mirek      int  `json:"mirek"`
		MirekValid bool `json:"mirek_valid"`
	}{Mirek: mirek, MirekValid: true}
	if withColor {
		l.Color = &struct {
			XY struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"xy"`
		}{}
		l.Color.XY.X = 0.4
		l.Color.XY.Y = 0.3
	}
	return l
}

func TestLightColorTempControl(t *testing.T) {
	fb := newFakeBridge(t,
		[]hue.Light{testLight("l1", "Lamp", "dev1", true, 80)},
		[]hue.Group{testRoom("room1", "Living", "dev1")},
		map[string]hue.GroupedLight{"gl-room1": testGroupedLight("gl-room1", true, 70)},
	)
	s := newLightsServer(t, fb)

	conn, _ := testConn(t)
	s.handleControlMessage(conn, []byte(`{"type":"light_color_temp","rid":"l1","mirek":300}`))

	fb.mu.Lock()
	body, ok := fb.lastLightPUT["l1"]
	fb.mu.Unlock()
	if !ok {
		t.Fatal("no PUT recorded for l1")
	}
	ct, ok := body["color_temperature"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body has no color_temperature: %v", body)
	}
	if v, ok := ct["mirek"].(float64); !ok || v != 300 {
		t.Fatalf("color_temperature.mirek = %v (%T), want 300", ct["mirek"], ct["mirek"])
	}
	if _, hasColor := body["color"]; hasColor {
		t.Fatalf("light_color_temp must not send a color key: %v", body)
	}
}

func TestLightsAPICarriesColorTemp(t *testing.T) {
	l1 := testCTLight("l1", "Ambiance", "dev1", 270, false) // white-ambiance: CT only
	l1.Metadata.Archetype = "ceiling_round"
	fb := newFakeBridge(t,
		[]hue.Light{
			l1,
			testCTLight("l2", "Color", "dev2", 300, true), // full-color bulb in CT mode
		},
		[]hue.Group{testRoom("room1", "Living", "dev1", "dev2")},
		map[string]hue.GroupedLight{"gl-room1": testGroupedLight("gl-room1", true, 70)},
	)
	s := newLightsServer(t, fb)

	rec := httptest.NewRecorder()
	s.handleLights(rec, httptest.NewRequest("GET", "/api/lights", nil))
	if rec.Code != 200 {
		t.Fatalf("handleLights status = %d, body %s", rec.Code, rec.Body.String())
	}
	var list []lightctl.Light
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode /api/lights: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d lights, want 2", len(list))
	}
	if !list[0].CTCapable || list[0].Colorable || list[0].Mirek != 270 {
		t.Fatalf("white-ambiance light: ct_capable=%v colorable=%v mirek=%d, want true/false/270",
			list[0].CTCapable, list[0].Colorable, list[0].Mirek)
	}
	if list[0].Archetype != "ceiling_round" {
		t.Fatalf("white-ambiance light archetype = %q, want ceiling_round", list[0].Archetype)
	}
	if !list[1].CTCapable || !list[1].Colorable || list[1].Mirek != 300 {
		t.Fatalf("color bulb: ct_capable=%v colorable=%v mirek=%d, want true/true/300",
			list[1].CTCapable, list[1].Colorable, list[1].Mirek)
	}
}

func TestRoomsAPICarriesArchetype(t *testing.T) {
	room := testRoom("room1", "Living", "dev1")
	room.Metadata.Archetype = "living_room"
	fb := newFakeBridge(t,
		[]hue.Light{testLight("l1", "Lamp", "dev1", true, 80)},
		[]hue.Group{room},
		map[string]hue.GroupedLight{"gl-room1": testGroupedLight("gl-room1", true, 70)},
	)
	s := newLightsServer(t, fb)

	rec := httptest.NewRecorder()
	s.handleRooms(rec, httptest.NewRequest("GET", "/api/rooms", nil))
	if rec.Code != 200 {
		t.Fatalf("handleRooms status = %d, body %s", rec.Code, rec.Body.String())
	}
	var list []lightctl.Room
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode /api/rooms: %v", err)
	}
	if len(list) != 1 || list[0].Archetype != "living_room" {
		t.Fatalf("rooms = %+v, want one room with archetype living_room", list)
	}
}
