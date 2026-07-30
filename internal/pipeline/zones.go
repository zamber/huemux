package pipeline

import (
	"sort"

	"github.com/zamber/huemux/internal/hue"
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

// Axis names a Hue position component. Which physical axis plays which role
// (screen-horizontal, screen-vertical, "depth") is configurable per area —
// see ZoneOpts — rather than hard-wired, because real entertainment areas
// don't agree on which axis carries useful information. A pair of Play bars
// either side of a monitor plus a gradient strip above it, for example, has
// meaningful x (left/right) and z (height) but a y (room depth) that is
// nearly constant across every channel, since they're all mounted right at
// the screen.
type Axis string

const (
	AxisX Axis = "x"
	AxisY Axis = "y"
	AxisZ Axis = "z"
)

func axisValue(p hue.Position, a Axis) float64 {
	switch a {
	case AxisY:
		return p.Y
	case AxisZ:
		return p.Z
	default:
		return p.X
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

	// AxisHorizontal, AxisVertical and AxisDepth assign each of a channel's
	// three position components to a role: which one becomes screen-U,
	// which becomes screen-V, and which (not a screen position at all) scales
	// how large a region is sampled. Implementations in the wild disagree
	// about axis conventions and it has not been consistent across app
	// versions, so rather than guess, this is configurable and the
	// calibration view is what settles it in ten seconds instead of a forum
	// thread. Defaults: horizontal=x, vertical=z, depth=y — height is almost
	// always the intuitive screen-vertical axis; room depth is what tends to
	// carry the least useful information for a screen-adjacent area.
	AxisHorizontal   Axis
	AxisVertical     Axis
	AxisDepth        Axis
	InvertHorizontal bool
	InvertVertical   bool
	InvertDepth      bool

	// DepthSizeGain scales sample rect size by how far a channel sits along
	// the depth axis: 0 disables the effect entirely (every channel samples
	// the same size region), higher values make a channel further along
	// the depth axis sample a larger, more averaged region. This is a
	// judgment call, not something Hue or other sync tools document — the
	// reasoning is that a light physically further from the screen tends to
	// contribute more to general room wash than to a precise accent, so
	// averaging more of the frame suits it better.
	DepthSizeGain float64

	// Feather softens rect edges so a zone that falls between grid cells does
	// not snap and judder as content moves. Fraction of the screen.
	Feather float64
}

func DefaultZoneOpts(configurationType string) ZoneOpts {
	return ZoneOpts{
		Mode:           DefaultModeFor(configurationType),
		EdgeWidth:      0.15,
		QuadrantSize:   0.35,
		AxisHorizontal: AxisX,
		AxisVertical:   AxisZ,
		AxisDepth:      AxisY,
		// Screen-V follows image convention (0=top, 1=bottom); Hue's z
		// (height) follows the physical convention (larger=higher). Left
		// un-inverted, a channel mounted higher up would sample toward the
		// bottom of the screen. x needs no equivalent flip: larger x is
		// already "further right," which already matches screen-U.
		InvertVertical: true,
		DepthSizeGain:  0.3,
		Feather:        0.04,
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
		// Order by the horizontal axis so the spread is spatially coherent
		// where positions do carry some meaning, and stable where they do not.
		idx := make([]int, len(channels))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			return axisValue(channels[idx[a]].Position, o.AxisHorizontal) < axisValue(channels[idx[b]].Position, o.AxisHorizontal)
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
		u, v, depth := project(c.Position, o)
		// depth is -1..+1; fold to a 1..(1+gain) multiplier so gain=0 is a
		// true no-op regardless of where a channel sits on the depth axis.
		sizeMul := 1 + o.DepthSizeGain*((depth+1)/2)

		var z Zone
		switch o.Mode {
		case ModeGlobal:
			z = Zone{U0: 0, V0: 0, U1: 1, V1: 1}
		case ModeQuadrant:
			h := (o.QuadrantSize * sizeMul) / 2
			z = Zone{U0: u - h, V0: v - h, U1: u + h, V1: v + h}
		default: // ModeEdges
			z = edgeRect(u, v, o.EdgeWidth, sizeMul)
		}
		z.ChannelID = c.ChannelID
		z.Feather = o.Feather
		z.LightRID = firstLight(c)
		zones = append(zones, clampZone(z))
	}
	return zones
}

// project maps a Hue position onto normalised screen coordinates plus a
// depth value, using the axis roles configured in o. See ZoneOpts for why
// this is configurable rather than hard-wired to x/y.
func project(p hue.Position, o ZoneOpts) (u, v, depth float64) {
	h := axisValue(p, o.AxisHorizontal)
	if o.InvertHorizontal {
		h = -h
	}
	vv := axisValue(p, o.AxisVertical)
	if o.InvertVertical {
		vv = -vv
	}
	d := axisValue(p, o.AxisDepth)
	if o.InvertDepth {
		d = -d
	}
	return clamp01((h + 1) / 2), clamp01((vv + 1) / 2), clampf(d, -1, 1)
}

// edgeRect snaps a position to the nearest screen edge and returns a band
// running along that edge, centred on the position. sizeMul scales both the
// band's thickness and its extent along the edge, e.g. for a channel that
// sits further along the configured depth axis.
func edgeRect(u, v, w, sizeMul float64) Zone {
	w *= sizeMul

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
	span := 0.30 * sizeMul
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
