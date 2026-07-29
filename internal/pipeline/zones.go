package pipeline

import (
	"sort"

	"lights.lan/lightsync/internal/hue"
)

// SampleMode mirrors the ways Hue's own sync products treat an area.
type SampleMode string

const (
	// ModeEdges samples a band at the screen edge nearest each channel.
	// The default for screen and monitor areas: play bars and strips.
	ModeEdges SampleMode = "edges"
	// ModeQuadrant samples a rectangle centred on the channel's mapped position.
	ModeQuadrant SampleMode = "quadrant"
	// ModeGlobal gives every channel the whole-screen average.
	ModeGlobal SampleMode = "global"
	// ModeSpread distributes channels evenly left to right, ignoring positions.
	// The right choice for music, 3dspace and other areas, where the positions
	// are not screen-relative in any useful sense.
	ModeSpread SampleMode = "spread"
)

// DefaultModeFor picks a sensible mode from the area's configuration_type.
func DefaultModeFor(configurationType string) SampleMode {
	switch configurationType {
	case "screen", "monitor":
		return ModeEdges
	default:
		return ModeSpread
	}
}

// ZoneOpts controls how positions become rects.
type ZoneOpts struct {
	Mode SampleMode
	// EdgeWidth is the thickness of the sampled band as a fraction of the
	// screen, used by ModeEdges. 0.15 is a good default.
	EdgeWidth float64
	// QuadrantSize is the side length of a ModeQuadrant rect, as a fraction.
	QuadrantSize float64
	// InvertDepth flips the depth axis.
	//
	// Implementations in the wild disagree about which end of the y axis is the
	// back of the room, and it has not been consistent across app versions.
	// Rather than guess, expose the flip and let the calibration view settle it.
	InvertDepth bool
	// Feather softens rect edges so a zone that falls between grid cells does
	// not snap and judder as content moves. Fraction of the screen.
	Feather float64
}

func DefaultZoneOpts(configurationType string) ZoneOpts {
	return ZoneOpts{
		Mode:         DefaultModeFor(configurationType),
		EdgeWidth:    0.15,
		QuadrantSize: 0.35,
		Feather:      0.04,
	}
}

// Zone is a sampling rect in normalised screen space.
// u runs 0 (left) to 1 (right); v runs 0 (top) to 1 (bottom).
type Zone struct {
	ChannelID uint8   `json:"ChannelID"`
	U0        float64 `json:"U0"`
	V0        float64 `json:"V0"`
	U1        float64 `json:"U1"`
	V1        float64 `json:"V1"`
	Feather   float64 `json:"Feather"`
	// LightRID is the first member's light resource, used for click-to-blink.
	LightRID string `json:"LightRID"`
}

// BuildZones maps entertainment channels onto screen rects.
//
// Channels, not lights: a gradient lightstrip is a single device contributing
// several channels, each with its own position, and each must sample a
// different part of the screen or the gradient is pointless.
func BuildZones(channels []hue.EntertainmentChannel, o ZoneOpts) []Zone {
	if len(channels) == 0 {
		return nil
	}

	zones := make([]Zone, 0, len(channels))

	if o.Mode == ModeSpread {
		// Order by x so the spread is spatially coherent where positions do
		// carry some meaning, and stable where they do not.
		idx := make([]int, len(channels))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			return channels[idx[a]].Position.X < channels[idx[b]].Position.X
		})
		w := 1.0 / float64(len(channels))
		for slot, i := range idx {
			c := channels[i]
			zones = append(zones, Zone{
				ChannelID: c.ChannelID,
				U0:        float64(slot) * w,
				U1:        float64(slot+1) * w,
				V0:        0,
				V1:        1,
				Feather:   o.Feather,
				LightRID:  firstLight(c),
			})
		}
		return zones
	}

	for _, c := range channels {
		u, v := project(c.Position, o.InvertDepth)

		var z Zone
		switch o.Mode {
		case ModeGlobal:
			z = Zone{U0: 0, V0: 0, U1: 1, V1: 1}
		case ModeQuadrant:
			h := o.QuadrantSize / 2
			z = Zone{U0: u - h, V0: v - h, U1: u + h, V1: v + h}
		default: // ModeEdges
			z = edgeRect(u, v, o.EdgeWidth)
		}
		z.ChannelID = c.ChannelID
		z.Feather = o.Feather
		z.LightRID = firstLight(c)
		zones = append(zones, clampZone(z))
	}
	return zones
}

// project maps a Hue position onto normalised screen coordinates.
//
// Hue positions are roughly -1..+1 with x running left to right and y running
// along the depth axis of the room. The convention users expect, and the one
// Hue's own sync products implement, is that a light placed at the back-left of
// the room pulls colour from the top-left of the screen.
func project(p hue.Position, invertDepth bool) (u, v float64) {
	y := p.Y
	if invertDepth {
		y = -y
	}
	u = (p.X + 1) / 2
	v = (y + 1) / 2
	return clamp01(u), clamp01(v)
}

// edgeRect snaps a position to the nearest screen edge and returns a band
// running along that edge, centred on the position.
func edgeRect(u, v, w float64) Zone {
	// Distance to each edge; smallest wins.
	dl, dr, dt, db := u, 1-u, v, 1-v
	min := dl
	edge := "l"
	if dr < min {
		min, edge = dr, "r"
	}
	if dt < min {
		min, edge = dt, "t"
	}
	if db < min {
		edge = "b"
	}

	// Extent along the edge, centred on the position, half a band wide either
	// side plus a little, so neighbouring channels overlap slightly instead of
	// leaving seams.
	const span = 0.30
	switch edge {
	case "l":
		return Zone{U0: 0, U1: w, V0: v - span/2, V1: v + span/2}
	case "r":
		return Zone{U0: 1 - w, U1: 1, V0: v - span/2, V1: v + span/2}
	case "t":
		return Zone{V0: 0, V1: w, U0: u - span/2, U1: u + span/2}
	default:
		return Zone{V0: 1 - w, V1: 1, U0: u - span/2, U1: u + span/2}
	}
}

func clampZone(z Zone) Zone {
	z.U0, z.U1 = clamp01(z.U0), clamp01(z.U1)
	z.V0, z.V1 = clamp01(z.V0), clamp01(z.V1)
	if z.U1 <= z.U0 {
		z.U1 = z.U0 + 0.02
	}
	if z.V1 <= z.V0 {
		z.V1 = z.V0 + 0.02
	}
	return z
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func firstLight(c hue.EntertainmentChannel) string {
	if len(c.Members) == 0 {
		return ""
	}
	return c.Members[0].Service.RID
}
