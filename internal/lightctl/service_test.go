package lightctl

import "testing"

// translateUpdate is the eventstream-delta → LightEvent translation. These
// tests pin the color-temperature handling added for the white-temperature
// picker: only mirek_valid:true deltas may emit Mirek, and the pre-existing
// on/dimming/color parsing must keep working alongside it.

func TestTranslateUpdateColorTemperature(t *testing.T) {
	// JSON numbers from the eventstream decode as float64 — the shape the
	// real path sees.
	ev, ok := translateUpdate(map[string]any{
		"type": "light", "id": "l1",
		"color_temperature": map[string]any{"mirek": float64(300), "mirek_valid": true},
	})
	if !ok {
		t.Fatal("translateUpdate rejected a valid light delta")
	}
	if ev.Mirek == nil || *ev.Mirek != 300 {
		t.Fatalf("Mirek = %v, want 300", ev.Mirek)
	}
}

func TestTranslateUpdateColorTemperatureInvalidDropped(t *testing.T) {
	// A switch back to color mode usually arrives as color_temperature with
	// mirek_valid:false. Emitting that would clobber the frontend's mirek
	// with a value the bridge itself no longer considers current.
	ev, ok := translateUpdate(map[string]any{
		"type": "light", "id": "l1",
		"color_temperature": map[string]any{"mirek": float64(400), "mirek_valid": false},
	})
	if !ok {
		t.Fatal("translateUpdate rejected a valid light delta")
	}
	if ev.Mirek != nil {
		t.Fatalf("mirek_valid:false must not emit Mirek, got %d", *ev.Mirek)
	}
}

func TestTranslateUpdateAbsentColorTemperature(t *testing.T) {
	ev, ok := translateUpdate(map[string]any{
		"type": "light", "id": "l1",
		"on": map[string]any{"on": true},
	})
	if !ok {
		t.Fatal("translateUpdate rejected a valid light delta")
	}
	if ev.Mirek != nil {
		t.Fatalf("no color_temperature in delta must not emit Mirek, got %d", *ev.Mirek)
	}
	if ev.On == nil || !*ev.On {
		t.Fatal("On not parsed when color_temperature is absent")
	}
}

func TestTranslateUpdateColorStillParsed(t *testing.T) {
	ev, ok := translateUpdate(map[string]any{
		"type": "light", "id": "l1",
		"color": map[string]any{"xy": map[string]any{"x": float64(0.4), "y": float64(0.3)}},
	})
	if !ok {
		t.Fatal("translateUpdate rejected a valid light delta")
	}
	if ev.X == nil || *ev.X != 0.4 || ev.Y == nil || *ev.Y != 0.3 {
		t.Fatalf("xy not parsed: X=%v Y=%v", ev.X, ev.Y)
	}
	if ev.Mirek != nil {
		t.Fatalf("xy-only delta must not emit Mirek, got %d", *ev.Mirek)
	}
}
