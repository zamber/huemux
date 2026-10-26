// Package muxrun owns the clocks around a mux.Program: one preview ticker
// that always runs so the editor animates, and one bridge-facing output
// worker that only runs while started. The Program itself stays pure; this
// is the only package in the mux stack that knows about wall-clock time,
// goroutines and I/O.
package muxrun

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zamber/huemux/internal/mux"
)

// Sink receives one frame's final per-light state. Implementations must
// respect the context and must not retain the map.
type Sink interface {
	Apply(ctx context.Context, out map[string]mux.LightState) error
}

// Providers inject the optional audio/video sources. A nil provider reads as
// inactive, which is the honest state under a lights-only profile.
type Providers struct {
	Audio func() mux.AudioInput
	Video func() mux.VideoInput
}

// LightView is one light in a preview snapshot.
type LightView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	On         bool    `json:"on"`
	Brightness float64 `json:"brightness"` // 0..1
	Hex        string  `json:"hex"`
}

// Snapshot is the latest preview frame, safe to encode as JSON.
type Snapshot struct {
	Tick    uint64                      `json:"tick"`
	At      time.Time                   `json:"at"`
	Running bool                        `json:"running"`
	Lanes   map[string]mux.SourceOutput `json:"lanes,omitempty"`
	Lights  []LightView                 `json:"lights"`
	Error   string                      `json:"error,omitempty"`
}

// Runtime schedules a Program and feeds a Sink.
type Runtime struct {
	mu       sync.RWMutex
	program  *mux.Program
	lights   []mux.Light
	sink     Sink
	provider Providers
	running  bool
	lastOut  time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	outCh chan map[string]mux.LightState

	latest atomic.Pointer[Snapshot]
	tick   atomic.Uint64

	errMu   sync.RWMutex
	lastErr string
}

// New starts the preview clock. The output clock is idle until Start.
func New(lights []mux.Light, sink Sink, provider Providers) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{
		lights:   lights,
		sink:     sink,
		provider: provider,
		ctx:      ctx,
		cancel:   cancel,
		outCh:    make(chan map[string]mux.LightState, 1),
	}
	r.wg.Add(2)
	go r.previewLoop()
	go r.outputLoop()
	return r
}

// Close stops both clocks. Safe to call more than once.
func (r *Runtime) Close() {
	r.cancel()
	r.wg.Wait()
}

// SetProgram swaps the compiled document. It does not change the running
// state: editing while stopped keeps previewing, editing while running swaps
// on the next tick.
func (r *Runtime) SetProgram(p *mux.Program) {
	r.mu.Lock()
	r.program = p
	if p != nil {
		r.lights = p.Lights
	}
	r.mu.Unlock()
}

// Program returns the current compiled document, if any.
func (r *Runtime) Program() *mux.Program {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.program
}

// SetLights updates the preview inventory without changing the program.
func (r *Runtime) SetLights(lights []mux.Light) {
	r.mu.Lock()
	r.lights = lights
	r.mu.Unlock()
}

// Start enables bridge output. The preview clock always runs.
func (r *Runtime) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.program == nil {
		return errNoProgram
	}
	r.running = true
	r.lastOut = time.Time{} // send the first frame immediately
	return nil
}

// Stop disables bridge output. The preview keeps animating.
func (r *Runtime) Stop() {
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
}

// Running reports whether bridge output is enabled.
func (r *Runtime) Running() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.running
}

// LastError is the most recent sink error, or "".
func (r *Runtime) LastError() string {
	r.errMu.RLock()
	defer r.errMu.RUnlock()
	return r.lastErr
}

// Snapshot returns the newest rendered frame.
func (r *Runtime) Snapshot() Snapshot {
	if p := r.latest.Load(); p != nil {
		s := *p
		s.Running = r.Running()
		s.Error = r.LastError()
		return s
	}
	r.mu.RLock()
	lights := r.lights
	r.mu.RUnlock()
	out := Snapshot{Lights: make([]LightView, 0, len(lights))}
	for _, l := range lights {
		out.Lights = append(out.Lights, LightView{ID: l.ID, Name: l.Name, X: l.X, Y: l.Y, Hex: "#000000"})
	}
	out.Running = r.Running()
	out.Error = r.LastError()
	return out
}

// RenderNow evaluates the program once at now and stores the snapshot. It is
// used by tests and by the "preview once" API; the background clock does the
// same thing on a ticker.
func (r *Runtime) RenderNow(now time.Time) {
	r.render(now)
}

func (r *Runtime) previewLoop() {
	defer r.wg.Done()
	// 12.5 Hz: fast enough that a pulse looks alive in the browser, cheap
	// enough that a phone does not spend the session waking for it.
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-t.C:
			r.render(now)
		}
	}
}

func (r *Runtime) outputLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case out := <-r.outCh:
			r.mu.RLock()
			sink := r.sink
			r.mu.RUnlock()
			if sink == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(r.ctx, 2500*time.Millisecond)
			err := sink.Apply(ctx, out)
			cancel()
			r.errMu.Lock()
			if err != nil {
				r.lastErr = err.Error()
			} else {
				r.lastErr = ""
			}
			r.errMu.Unlock()
		}
	}
}

func (r *Runtime) render(now time.Time) {
	r.mu.RLock()
	p := r.program
	running := r.running
	sink := r.sink
	provider := r.provider
	lights := r.lights
	r.mu.RUnlock()
	if p == nil {
		return
	}
	in := mux.Inputs{Now: now}
	if provider.Audio != nil {
		in.Audio = provider.Audio()
	}
	if provider.Video != nil {
		in.Video = provider.Video()
	}
	res := p.Step(in)

	snap := Snapshot{
		Tick:    r.tick.Add(1),
		At:      now,
		Running: running,
		Lanes:   res.LaneValues,
		Lights:  make([]LightView, 0, len(lights)),
	}
	for _, l := range lights {
		st := res.Output[l.ID]
		v := LightView{ID: l.ID, Name: l.Name, X: l.X, Y: l.Y, On: st.On, Brightness: st.Brightness}
		if st.On {
			v.Hex = st.Color.Hex()
		} else {
			v.Hex = "#000000"
		}
		snap.Lights = append(snap.Lights, v)
	}
	r.latest.Store(&snap)

	if !running || sink == nil {
		return
	}
	hz := p.Output.Hz
	if hz <= 0 {
		hz = 4
	}
	r.mu.Lock()
	due := r.lastOut.IsZero() || now.Sub(r.lastOut) >= time.Second/time.Duration(hz)
	if due {
		r.lastOut = now
	}
	r.mu.Unlock()
	if !due {
		return
	}
	// Latest frame wins: a slow bridge must not build a queue of stale
	// frames. Dropping the oldest is correct here; catching up is not.
	select {
	case r.outCh <- res.Output:
	default:
		select {
		case <-r.outCh:
		default:
		}
		select {
		case r.outCh <- res.Output:
		default:
		}
	}
}

type runtimeError string

func (e runtimeError) Error() string { return string(e) }

const errNoProgram runtimeError = "mux: no program loaded"
