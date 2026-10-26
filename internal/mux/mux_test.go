package mux

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func testLights(n int) []Light {
	out := make([]Light, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Light{
			ID:   string(rune('a' + i)),
			Name: string(rune('A' + i)),
			X:    float64(i) / float64(max(1, n-1)),
			Y:    0.5,
		})
	}
	return out
}

func TestHexRoundTrip(t *testing.T) {
	c, err := Hex("#FF7A18")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Hex(); got != "#FF7A18" {
		t.Fatalf("round trip: got %s", got)
	}
	if _, err := Hex("nope"); err == nil {
		t.Fatal("expected an error for a non-hex color")
	}
}

func TestBlendModes(t *testing.T) {
	red := MustHex("#FF0000")
	blue := MustHex("#0000FF")
	if got := BlendColor(Color{}, false, red, BlendAdd); got != red {
		t.Fatalf("first pixel should pass through: %+v", got)
	}
	if got := BlendColor(red, true, blue, BlendReplace); got != blue {
		t.Fatalf("replace should win: %+v", got)
	}
	if got := BlendColor(red, true, blue, BlendAdd); got.B < 0.9 || got.R < 0.9 {
		t.Fatalf("add should keep both channels: %+v", got)
	}
	if got := BlendColor(red, true, red, BlendMultiply); got.R < 0.99 {
		t.Fatalf("multiply red*red should stay red: %+v", got)
	}
}

func TestDefaultDocumentCompilesAndTicks(t *testing.T) {
	lights := testLights(3)
	doc := DefaultDocument(lights)
	prog, err := Compile(doc, lights)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res := prog.Step(Inputs{Now: time.Unix(100, 0)})
	if len(res.Output) != len(lights) {
		t.Fatalf("got %d outputs, want %d", len(res.Output), len(lights))
	}
	for _, l := range lights {
		if _, ok := res.Output[l.ID]; !ok {
			t.Fatalf("missing light %s", l.ID)
		}
	}
}

func TestCatalogDefaultsConstruct(t *testing.T) {
	for _, s := range Catalog().Sources {
		if _, err := NewSource(s.Type, nil); err != nil {
			t.Errorf("source %s: %v", s.Type, err)
		}
	}
	for _, e := range Catalog().Effects {
		if _, err := NewEffect(e.Type, nil); err != nil {
			t.Errorf("effect %s: %v", e.Type, err)
		}
	}
}

func TestCompileRejectsUnknowns(t *testing.T) {
	lights := testLights(2)
	badSource := Document{Version: 1, Name: "x", Lanes: []LaneDoc{{
		Source: NodeDoc{Type: "nope"}, Effect: NodeDoc{Type: "pulse"},
	}}}
	if _, err := Compile(badSource, lights); err == nil {
		t.Fatal("unknown source should fail")
	}
	badLight := Document{Version: 1, Name: "x", Lights: []string{"zzz"}, Lanes: []LaneDoc{{
		Source: NodeDoc{Type: "timer"}, Effect: NodeDoc{Type: "pulse"},
	}}}
	if _, err := Compile(badLight, lights); err == nil {
		t.Fatal("unknown target light should fail")
	}
}

func TestTimerAndPulseCompose(t *testing.T) {
	src, err := NewSource("timer", map[string]any{"bpm": 60.0, "waveform": "sine", "offset": 0.25})
	if err != nil {
		t.Fatal(err)
	}
	eff, err := NewEffect("pulse", map[string]any{
		"color": "#FF0000", "source": "value", "min": 0.0, "max": 1.0, "curve": "lin",
	})
	if err != nil {
		t.Fatal(err)
	}
	l := []Light{{ID: "a", X: 0.5, Y: 0.5}}
	out := eff.Render(RenderInput{
		Now:    time.Unix(0, 0),
		Source: src.Sample(Inputs{Now: time.Unix(0, 0)}),
		Lights: l,
	})
	if math.Abs(out["a"].R-0.5) > 0.02 {
		t.Fatalf("expected a half-red pulse at phase 0.25, got %+v", out["a"])
	}
}

func TestVideoMapOnlyUsesKnownLights(t *testing.T) {
	eff, err := NewEffect("video_map", map[string]any{"gain": 1.0, "brightness": 1.0, "saturation": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	out := eff.Render(RenderInput{
		Source: SourceOutput{Colors: map[string]Color{"a": MustHex("#00FF00")}},
		Lights: []Light{{ID: "a"}, {ID: "b"}},
	})
	if len(out) != 1 || out["a"].G < 0.9 {
		t.Fatalf("unexpected video map: %+v", out)
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc := DefaultDocument(testLights(2))
	raw, _ := json.Marshal(doc)
	if err := s.Put("mine", raw); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get("mine"); !ok || len(got) == 0 {
		t.Fatal("stored preset not found")
	}
	if err := s.Put("salon_rainbow", raw); err != ErrBuiltinSlug {
		t.Fatalf("builtin write: got %v", err)
	}
	if err := s.SaveCurrent(raw); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.LoadCurrent(); !ok || len(got) == 0 {
		t.Fatal("current document not found")
	}
	if err := s.Delete("mine"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("mine"); ok {
		t.Fatal("deleted preset still found")
	}
}
