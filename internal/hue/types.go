// Package hue implements the pieces of the Philips Hue CLIP v2 REST API and
// the Entertainment DTLS streaming API that lightsync needs: pairing,
// reading entertainment configurations, and streaming colors.
package hue

// ResourceIdentifier is Hue's standard {rid, rtype} reference, used
// throughout CLIP v2 to point from one resource at another.
type ResourceIdentifier struct {
	RID   string `json:"rid"`
	RType string `json:"rtype"`
}

// Position is a Hue entertainment channel's position in the room, normalised
// to roughly -1..+1 on each axis. X runs left to right, Y is the depth axis
// (toward/away from the screen), Z is height.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// ChannelMember links a channel to the entertainment service (and, for a
// segmented device such as a gradient lightstrip, a segment index within it)
// that contributes to it.
type ChannelMember struct {
	Service ResourceIdentifier `json:"service"`
	Index   int                `json:"index"`
}

// EntertainmentChannel is one addressable channel within an entertainment
// configuration. A single device (e.g. a gradient lightstrip) can own
// several channels, each with its own position.
type EntertainmentChannel struct {
	ChannelID uint8           `json:"channel_id"`
	Position  Position        `json:"position"`
	Members   []ChannelMember `json:"members"`
}

// ServiceLocation gives the position(s) of one entertainment service. A
// segmented device contributes multiple positions.
type ServiceLocation struct {
	Service            ResourceIdentifier `json:"service"`
	Positions          []Position         `json:"positions"`
	EqualizationFactor float64            `json:"equalization_factor"`
}

// EntertainmentConfiguration is the GET
// /clip/v2/resource/entertainment_configuration/{id} shape lightsync reads.
type EntertainmentConfiguration struct {
	ID                string   `json:"id"`
	IDV1              string   `json:"id_v1,omitempty"`
	Type              string   `json:"type"`
	Metadata          Metadata `json:"metadata"`
	ConfigurationType string   `json:"configuration_type"`
	Status            string   `json:"status"`

	// ActiveStreamer is present only while some application holds the
	// stream. Its presence, not its content, is what matters.
	ActiveStreamer *ResourceIdentifier `json:"active_streamer,omitempty"`

	StreamProxy struct {
		Mode string             `json:"mode"`
		Node ResourceIdentifier `json:"node"`
	} `json:"stream_proxy"`

	Channels  []EntertainmentChannel `json:"channels"`
	Locations struct {
		ServiceLocations []ServiceLocation `json:"service_locations"`
	} `json:"locations"`
}

// Metadata carries the user-facing name Hue attaches to most resources.
type Metadata struct {
	Name string `json:"name"`
}

// IsActive reports whether another application is currently streaming to
// this area.
func (c EntertainmentConfiguration) IsActive() bool {
	return c.ActiveStreamer != nil
}
