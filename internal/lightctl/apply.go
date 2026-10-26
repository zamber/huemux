package lightctl

import "context"

// LightCommand is one combined light update: power, brightness and color in a
// single request. The mux output path needs this because three separate
// setters turn one frame into three bridge requests per light, which is
// enough to saturate the bridge before the effect is visible.
type LightCommand struct {
	On           bool
	Brightness   float64 // 0..100
	R, G, B      uint8
	TransitionMS int
}

// ApplyLight sends one combined state update to a light.
func (s *Service) ApplyLight(ctx context.Context, rid string, cmd LightCommand) error {
	brightness := cmd.Brightness
	if brightness < 0 {
		brightness = 0
	}
	if brightness > 100 {
		brightness = 100
	}
	x, y := rgbToXY(cmd.R, cmd.G, cmd.B)
	return s.client.SetLightCommand(ctx, rid, cmd.On, brightness, x, y, cmd.TransitionMS)
}
