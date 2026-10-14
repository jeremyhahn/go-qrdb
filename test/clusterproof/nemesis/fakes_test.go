//go:build clusterproof

package nemesis

import (
	"context"
	"slices"
	"sync"
	"syscall"
	"time"
)

// call is one primitive a fake target was asked to perform.
type call struct {
	Op   string // "exec", "signal", "restart" or "unseal"
	Argv []string
	Sig  syscall.Signal
}

// fakeTarget records every primitive and never touches the host. errs makes
// an op fail until the test clears it.
type fakeTarget struct {
	name string

	mu    sync.Mutex
	calls []call
	errs  map[string]error
}

var _ Target = (*fakeTarget)(nil)

func newFake(name string) *fakeTarget { return &fakeTarget{name: name, errs: map[string]error{}} }

func (f *fakeTarget) Name() string { return f.name }

func (f *fakeTarget) Exec(_ context.Context, argv []string) ([]byte, error) {
	return nil, f.record(call{Op: "exec", Argv: slices.Clone(argv)})
}

func (f *fakeTarget) Signal(_ context.Context, sig syscall.Signal) error {
	return f.record(call{Op: "signal", Sig: sig})
}

func (f *fakeTarget) record(c call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.errs[c.Op]
}

func (f *fakeTarget) fail(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, op)
		return
	}
	f.errs[op] = err
}

// take returns the calls recorded since the last take.
func (f *fakeTarget) take() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// fakeRestarter is a fakeTarget that can also be restarted.
type fakeRestarter struct{ *fakeTarget }

var _ Restarter = fakeRestarter{}

func newRestarter(name string) fakeRestarter { return fakeRestarter{newFake(name)} }

func (f fakeRestarter) Restart(context.Context) error { return f.record(call{Op: "restart"}) }

// fakeUnsealer is a fakeRestarter whose node comes back sealed.
type fakeUnsealer struct{ fakeRestarter }

var _ Unsealer = fakeUnsealer{}

func newUnsealer(name string) fakeUnsealer { return fakeUnsealer{newRestarter(name)} }

func (f fakeUnsealer) Unseal(context.Context) error { return f.record(call{Op: "unseal"}) }

// fakeRunner records the rendered argv a RemoteTarget runs.
type fakeRunner struct {
	mu   sync.Mutex
	runs [][]string
	out  []byte
	err  error
}

func (r *fakeRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, slices.Clone(argv))
	return r.out, r.err
}

// fakeClock jumps forward by whatever a caller waits for.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// fakeNemesis records Inject and Heal calls.
type fakeNemesis struct {
	name      string
	injectErr error
	healErr   error
	onInject  func()

	mu       sync.Mutex
	injected [][]string
	heals    int
	healCtx  []error // ctx.Err() seen by each heal
}

func (n *fakeNemesis) Name() string { return n.name }

func (n *fakeNemesis) Inject(_ context.Context, targets []Target) error {
	n.mu.Lock()
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name()
	}
	n.injected = append(n.injected, names)
	n.mu.Unlock()
	if n.onInject != nil {
		n.onInject()
	}
	return n.injectErr
}

func (n *fakeNemesis) Heal(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.heals++
	n.healCtx = append(n.healCtx, ctx.Err())
	return n.healErr
}

// targets converts fakes to the Target slice a nemesis takes.
func targets[T Target](ts ...T) []Target {
	out := make([]Target, len(ts))
	for i, t := range ts {
		out[i] = t
	}
	return out
}
