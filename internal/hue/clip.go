package hue

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a single bridge's CLIP v2 REST API.
//
// The bridge presents a self-signed certificate on HTTPS. The system trust
// store will never accept it, so verification is skipped here rather than
// pinned — this is a LAN device on the user's own network, not a public
// endpoint.
type Client struct {
	BridgeIP string
	Username string // application key; also the DTLS PSK identity

	hc *http.Client
}

// NewClient builds a client for a bridge that has already been paired.
// Username may be empty for calls made before pairing (Pair, BridgeConfig).
func NewClient(bridgeIP, username string) *Client {
	return &Client{
		BridgeIP: bridgeIP,
		Username: username,
		hc: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // bridge cert is self-signed; see doc comment above
			},
		},
	}
}

type v2Envelope[T any] struct {
	Errors []struct {
		Description string `json:"description"`
	} `json:"errors"`
	Data []T `json:"data"`
}

func (c *Client) doV2(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	url := fmt.Sprintf("https://%s%s", c.BridgeIP, path)
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("hue-application-key", c.Username)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: bridge returned %s: %s", method, path, resp.Status, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response from %s %s: %w (body: %s)", method, path, err, raw)
	}
	return nil
}

// BridgeInfo is the subset of the unauthenticated /api/config endpoint
// lightsync needs, mainly to reject bridges too old to support Entertainment.
type BridgeInfo struct {
	Name       string `json:"name"`
	ModelID    string `json:"modelid"`
	BridgeID   string `json:"bridgeid"`
	APIVersion string `json:"apiversion"`
}

// BridgeConfig fetches the unauthenticated bridge config. It works before
// pairing and is how discovery confirms an IP is actually a Hue bridge.
func BridgeConfig(ctx context.Context, bridgeIP string) (BridgeInfo, error) {
	c := NewClient(bridgeIP, "")
	var info BridgeInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://%s/api/0/config", bridgeIP), nil)
	if err != nil {
		return info, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return info, fmt.Errorf("contact bridge at %s: %w", bridgeIP, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return info, fmt.Errorf("decode bridge config: %w", err)
	}
	return info, nil
}

// SupportsEntertainment does a best-effort check that the bridge model is
// new enough for the Entertainment API. Old round (v1) bridges are not, and
// fail with a confusing timeout deep in the DTLS handshake if you let them
// get that far — better to say so up front.
func SupportsEntertainment(info BridgeInfo) bool {
	// Square (v2) bridges report modelid "BSB002". The old round bridge is
	// "BSB001" and does not support Entertainment areas at all.
	return info.ModelID != "BSB001"
}

// pairResult mirrors the legacy (v1, array-wrapped) /api response shape,
// which the bridge still uses for registration.
type pairResult struct {
	Success *struct {
		Username  string `json:"username"`
		ClientKey string `json:"clientkey"`
	} `json:"success"`
	Error *struct {
		Type        int    `json:"type"`
		Description string `json:"description"`
	} `json:"error"`
}

// linkButtonNotPressed is the legacy API's error type for "registration
// requested but the link button has not been pressed yet."
const linkButtonNotPressed = 101

// Pair registers lightsync with the bridge, polling every 2s while the user
// presses the physical link button, and gives up after timeout.
//
// generateclientkey is mandatory: without it the bridge issues a username
// with no PSK, which is enough for ordinary REST calls but useless for
// Entertainment streaming.
func Pair(ctx context.Context, bridgeIP, devicetype string, timeout time.Duration) (username, clientkey string, err error) {
	c := NewClient(bridgeIP, "")
	body := map[string]any{
		"devicetype":        devicetype,
		"generateclientkey": true,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}

	deadline := time.Now().Add(timeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://%s/api", bridgeIP), bytes.NewReader(b))
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.hc.Do(req)
		if err != nil {
			return "", "", fmt.Errorf("contact bridge at %s: %w", bridgeIP, err)
		}
		var results []pairResult
		decErr := json.NewDecoder(resp.Body).Decode(&results)
		resp.Body.Close()
		if decErr != nil {
			return "", "", fmt.Errorf("decode pairing response: %w", decErr)
		}

		if len(results) > 0 {
			if results[0].Success != nil {
				if results[0].Success.ClientKey == "" {
					return "", "", fmt.Errorf("bridge did not return a clientkey; generateclientkey may not have been honoured")
				}
				return results[0].Success.Username, results[0].Success.ClientKey, nil
			}
			if results[0].Error != nil && results[0].Error.Type != linkButtonNotPressed {
				return "", "", fmt.Errorf("bridge rejected pairing: %s", results[0].Error.Description)
			}
		}

		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("timed out waiting for the link button to be pressed (%s)", timeout)
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ListEntertainmentConfigurations fetches every entertainment area defined
// on the bridge.
func (c *Client) ListEntertainmentConfigurations(ctx context.Context) ([]EntertainmentConfiguration, error) {
	var env v2Envelope[EntertainmentConfiguration]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/entertainment_configuration", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// GetEntertainmentConfiguration fetches one area fresh — callers should call
// this whenever the area picker opens rather than caching it, since users
// rearrange areas in the Hue app and expect the change to show up.
func (c *Client) GetEntertainmentConfiguration(ctx context.Context, id string) (EntertainmentConfiguration, error) {
	var env v2Envelope[EntertainmentConfiguration]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/entertainment_configuration/"+id, nil, &env); err != nil {
		return EntertainmentConfiguration{}, err
	}
	if len(env.Data) == 0 {
		return EntertainmentConfiguration{}, fmt.Errorf("no entertainment configuration with id %s", id)
	}
	return env.Data[0], nil
}

// StartStreaming PUTs {"action":"start"}. This must happen before the DTLS
// handshake is attempted — the bridge only opens its DTLS listener for an
// area that is already in streaming state.
func (c *Client) StartStreaming(ctx context.Context, id string) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/entertainment_configuration/"+id,
		map[string]any{"action": "start"}, nil)
}

// StopStreaming PUTs {"action":"stop"} and releases the area for other
// applications.
func (c *Client) StopStreaming(ctx context.Context, id string) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/entertainment_configuration/"+id,
		map[string]any{"action": "stop"}, nil)
}

// SetOn turns a light on or off via its ordinary on/off state.
//
// This matters more than it sounds like it should: Entertainment streaming
// controls color and brightness, but it does not override a light's on/off
// state. A physically-off bulb receiving a perfect DTLS color stream stays
// dark and gives no error anywhere — exactly the kind of silent failure this
// whole project is trying to avoid, so callers that start a stream should
// make sure the lights are on first rather than leaving a user to discover
// this by trial and error.
func (c *Client) SetOn(ctx context.Context, lightRID string, on bool) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/light/"+lightRID,
		map[string]any{"on": map[string]any{"on": on}}, nil)
}

// entertainmentService is the subset of the `entertainment` resource needed
// to resolve a channel member's service back to the physical light it
// belongs to, for click-to-identify in the calibration UI.
//
// `owner` on this resource is the *device*, not the light — a device can
// have several services (entertainment, light, button, ...) and owner just
// names the parent. `renderer_reference` is what actually points at the
// `light` resource this entertainment service drives.
type entertainmentService struct {
	Owner             ResourceIdentifier `json:"owner"`
	RendererReference ResourceIdentifier `json:"renderer_reference"`
}

// ResolveLightForService walks from an entertainment channel member's
// service id to the light resource id it belongs to.
func (c *Client) ResolveLightForService(ctx context.Context, serviceRID string) (string, error) {
	var env v2Envelope[entertainmentService]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/entertainment/"+serviceRID, nil, &env); err != nil {
		return "", err
	}
	if len(env.Data) == 0 {
		return "", fmt.Errorf("no entertainment service with id %s", serviceRID)
	}
	if rid := env.Data[0].RendererReference.RID; rid != "" {
		return rid, nil
	}
	return env.Data[0].Owner.RID, nil
}

// Identify blinks a light briefly, so the calibration view can confirm a
// channel-to-screen-region mapping without the user walking to the wall.
func (c *Client) Identify(ctx context.Context, lightRID string) error {
	return c.doV2(ctx, http.MethodPut, "/clip/v2/resource/light/"+lightRID,
		map[string]any{"identify": map[string]any{"action": "identify"}}, nil)
}

// LightColorFeatures reports what a light service can actually do, so
// white-only bulbs can be driven on brightness alone instead of sitting at a
// fixed, useless color.
type LightColorFeatures struct {
	SupportsColor      bool
	SupportsBrightness bool
}

type lightResource struct {
	Color     *struct{} `json:"color,omitempty"`
	Dimming   *struct{} `json:"dimming,omitempty"`
	ColorTemp *struct{} `json:"color_temperature,omitempty"`
}

// GetLightFeatures fetches which capabilities a light resource supports.
func (c *Client) GetLightFeatures(ctx context.Context, lightRID string) (LightColorFeatures, error) {
	var env v2Envelope[lightResource]
	if err := c.doV2(ctx, http.MethodGet, "/clip/v2/resource/light/"+lightRID, nil, &env); err != nil {
		return LightColorFeatures{}, err
	}
	if len(env.Data) == 0 {
		return LightColorFeatures{}, fmt.Errorf("no light with id %s", lightRID)
	}
	l := env.Data[0]
	return LightColorFeatures{
		SupportsColor:      l.Color != nil,
		SupportsBrightness: l.Dimming != nil,
	}, nil
}
