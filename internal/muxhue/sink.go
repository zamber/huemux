// Package muxhue adapts a mux output frame to Hue light commands. It is the
// only place in the mux stack that knows the bridge exists; the core and the
// runtime deal in linear RGB and a Sink interface.
package muxhue

import (
	"context"
	"fmt"
	"sync"

	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/mux"
)

// Applier is the narrow slice of lightctl the sink needs. Keeping it an
// interface here lets tests pass a recorder and keeps this package from
// growing a dependency on the rest of light control.
type Applier interface {
	ApplyLight(ctx context.Context, rid string, cmd lightctl.LightCommand) error
}

// Sink writes changed lights to the bridge. It skips lights whose quantized
// state did not change, which is most of them on a slow gradient.
type Sink struct {
	applier      Applier
	transitionMS int
	concurrency  int

	mu   sync.Mutex
	last map[string]string
}

// New builds a sink. A non-positive concurrency defaults to 4: enough to
// hide per-request latency, low enough not to swamp a bridge that is already
// serving the light-control page.
func New(applier Applier, transitionMS, concurrency int) *Sink {
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Sink{
		applier:      applier,
		transitionMS: transitionMS,
		concurrency:  concurrency,
		last:         map[string]string{},
	}
}

// Reset forgets the last-sent cache. Call it when mux output (re)starts, so
// a light changed from elsewhere is written on the first frame.
func (s *Sink) Reset() {
	s.mu.Lock()
	s.last = map[string]string{}
	s.mu.Unlock()
}

// Apply sends one frame. Lights with an unchanged quantized state are
// skipped. It returns the first error but still attempts every other light:
// one unreachable lamp should not blank the room.
func (s *Sink) Apply(ctx context.Context, out map[string]mux.LightState) error {
	type job struct {
		rid string
		cmd lightctl.LightCommand
		key string
	}
	jobs := make([]job, 0, len(out))
	s.mu.Lock()
	for rid, st := range out {
		r, g, b := st.Color.SRGB8()
		brightness := st.Brightness * 100
		if !st.On {
			brightness = 0
			r, g, b = 0, 0, 0
		}
		key := fmt.Sprintf("%t|%d|%02x%02x%02x", st.On, int(brightness+0.5), r, g, b)
		if s.last[rid] == key {
			continue
		}
		jobs = append(jobs, job{
			rid: rid,
			key: key,
			cmd: lightctl.LightCommand{
				On: st.On, Brightness: brightness,
				R: r, G: g, B: b, TransitionMS: s.transitionMS,
			},
		})
	}
	s.mu.Unlock()

	if len(jobs) == 0 {
		return nil
	}

	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for _, j := range jobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.applier.ApplyLight(ctx, j.rid, j.cmd); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("light %s: %w", j.rid, err)
				}
				errMu.Unlock()
				return
			}
			s.mu.Lock()
			s.last[j.rid] = j.key
			s.mu.Unlock()
		}()
	}
	wg.Wait()
	return firstErr
}
