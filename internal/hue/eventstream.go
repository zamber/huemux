package hue

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Event is one batch of resource updates from the bridge's CLIP v2
// eventstream. Each Data item is a *partial* resource — only the fields
// that actually changed are present (confirmed live: toggling a light's
// on/off produces a data item containing just {"id","type","on":{...},
// "owner"}, not the light's other fields) — so callers must merge these
// into cached state field-by-field, never treat one as a full resource.
type Event struct {
	CreationTime string           `json:"creationtime"`
	Data         []map[string]any `json:"data"`
	ID           string           `json:"id"`
	Type         string           `json:"type"` // "update" | "add" | "delete" | "error"
}

// SubscribeEvents connects to the bridge's CLIP v2 eventstream and keeps
// reconnecting for as long as ctx is alive, so callers get one stable
// channel for the app's lifetime rather than having to manage reconnection
// themselves. Backoff is exponential, capped at 30s (same policy
// ROADMAP.md prescribes for the DTLS stream's reconnect), and resets once
// a connection has stayed up for at least 30s — a flappy bridge shouldn't
// make every subsequent retry wait the full cap.
func (c *Client) SubscribeEvents(ctx context.Context) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		backoff := time.Second
		for ctx.Err() == nil {
			connectedAt := time.Now()
			err := c.streamEventsOnce(ctx, out)
			if ctx.Err() != nil {
				return
			}
			if time.Since(connectedAt) > 30*time.Second {
				backoff = time.Second
			}
			if err != nil {
				log.Printf("lightsync: eventstream disconnected: %v; retrying in %s", err, backoff)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}()
	return out
}

// streamEventsOnce holds one eventstream connection open until it errors,
// the bridge closes it, or ctx is cancelled, decoding each `data: [...]`
// line as it arrives and sending its events to out.
func (c *Client) streamEventsOnce(ctx context.Context, out chan<- Event) error {
	// The eventstream is long-lived by design, so it needs its own client:
	// c.hc (used for ordinary REST calls elsewhere in this package) has a
	// 10s total-request timeout, which would kill this connection almost
	// immediately.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // bridge cert is self-signed, see clip.go
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://%s/eventstream/clip/v2", c.BridgeIP), nil)
	if err != nil {
		return err
	}
	req.Header.Set("hue-application-key", c.Username)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("bridge returned %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // a batch of many lights changing at once can be a large single line
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "", strings.HasPrefix(line, ":"), strings.HasPrefix(line, "id:"):
			continue // blank/keepalive-comment/id lines carry nothing we need
		case strings.HasPrefix(line, "data:"):
			var events []Event
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if err := json.Unmarshal([]byte(payload), &events); err != nil {
				log.Printf("lightsync: eventstream: bad event payload: %v", err)
				continue
			}
			for _, e := range events {
				select {
				case out <- e:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return fmt.Errorf("stream closed by bridge")
}
