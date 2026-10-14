//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"sync"
	"syscall"
)

// Nemesis is one kind of fault.
type Nemesis interface {
	// Name identifies the fault in schedules and the history.
	Name() string
	// Inject applies the fault to targets. A failed Inject may have touched
	// some of them; Heal undoes whatever it touched.
	Inject(ctx context.Context, targets []Target) error
	// Heal undoes the last Inject on every target it touched or tried to
	// touch, including after an Inject that failed. It is idempotent: a
	// second Heal, or a Heal with nothing injected, does nothing. A target
	// that fails to heal stays pending and the next Heal retries it.
	Heal(ctx context.Context) error
}

// step is one fault action on one target.
type step func(ctx context.Context, t Target) error

// fault is the engine every nemesis here is built on: it records which
// targets an Inject touched, so Heal reaches exactly those, before and after
// a failure.
//
// A mutex rather than an atomic: an Inject and a Heal must not interleave on
// the node, not only in the bookkeeping, so the steps themselves are
// serialised.
type fault struct {
	name string
	// plan validates the targets before any is touched and returns the
	// inject step for this Inject.
	plan func(ctx context.Context, targets []Target) (step, error)
	heal step

	mu      sync.Mutex
	pending []Target
}

var _ Nemesis = (*fault)(nil)

func (f *fault) Name() string { return f.name }

func (f *fault) Inject(ctx context.Context, targets []Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) > 0 {
		return ErrUnhealed
	}
	if len(targets) == 0 {
		return ErrNoTargets
	}
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t.Name()] {
			return &ConfigError{Field: f.name + " targets", Reason: "node " + t.Name() + " named twice"}
		}
		seen[t.Name()] = true
	}
	inject, err := f.plan(ctx, targets)
	if err != nil {
		return err
	}
	for _, t := range targets {
		err := inject(ctx, t)
		// A target that refused the primitive was not touched; every other
		// outcome, failure included, may have changed the node.
		if errors.Is(err, ErrUnsupported) {
			return &FaultError{Nemesis: f.name, Op: "inject", Node: t.Name(), Err: err}
		}
		f.pending = append(f.pending, t)
		if err != nil {
			return &FaultError{Nemesis: f.name, Op: "inject", Node: t.Name(), Err: err}
		}
	}
	return nil
}

func (f *fault) Heal(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var failed []Target
	var errs []error
	for _, t := range f.pending {
		if err := f.heal(ctx, t); err != nil {
			failed = append(failed, t)
			errs = append(errs, &FaultError{Nemesis: f.name, Op: "heal", Node: t.Name(), Err: err})
		}
	}
	f.pending = failed
	return errors.Join(errs...)
}

// staticPlan is the plan of a fault whose inject step does not depend on the
// target set.
func staticPlan(inject step) func(context.Context, []Target) (step, error) {
	return func(context.Context, []Target) (step, error) { return inject, nil }
}

// signalStep sends sig to the target.
func signalStep(sig syscall.Signal) step {
	return func(ctx context.Context, t Target) error { return t.Signal(ctx, sig) }
}

// execStep runs the target's command rendered from tmpl.
func execStep(tmpl *Template, data func(Target) (any, []string)) step {
	return func(ctx context.Context, t Target) error {
		d, inner := data(t)
		argv, err := tmpl.Render(d, inner)
		if err != nil {
			return err
		}
		_, err = t.Exec(ctx, argv)
		return err
	}
}

// Kill is the SIGKILL-and-restart fault: Inject kills each target's server
// process, Heal restarts it. Every target must be a [Restarter]; Inject
// checks that before it kills anything, because a kill that cannot heal
// destroys the node.
func Kill() Nemesis {
	return kill(func(t Target) bool {
		_, ok := t.(Restarter)
		return ok
	}, func(ctx context.Context, t Target) error {
		r, ok := t.(Restarter)
		if !ok {
			return ErrUnsupported
		}
		return r.Restart(ctx)
	})
}

// KillSealed is [Kill] for a cluster whose nodes restart sealed: Heal
// restarts each target and then unseals it with its own credential. Every
// target must be an [Unsealer], checked before anything is killed. A restart
// that succeeds and an unseal that fails leave the target pending, and the
// next Heal repeats both: each is a no-op on a node already past it.
func KillSealed() Nemesis {
	return kill(func(t Target) bool {
		_, ok := t.(Unsealer)
		return ok
	}, func(ctx context.Context, t Target) error {
		u, ok := t.(Unsealer)
		if !ok {
			return ErrUnsupported
		}
		if err := u.Restart(ctx); err != nil {
			return err
		}
		return u.Unseal(ctx)
	})
}

// kill is the SIGKILL fault whose targets must satisfy can and whose heal is
// heal.
func kill(can func(Target) bool, heal step) Nemesis {
	return &fault{
		name: "kill",
		plan: func(_ context.Context, targets []Target) (step, error) {
			for _, t := range targets {
				if !can(t) {
					return nil, &FaultError{Nemesis: "kill", Op: "inject", Node: t.Name(), Err: ErrUnsupported}
				}
			}
			return signalStep(syscall.SIGKILL), nil
		},
		heal: heal,
	}
}

// Pause is the SIGSTOP/SIGCONT fault: Inject stops each target's server
// process, Heal continues it. SIGCONT to a running process is a no-op, so
// the heal is idempotent on the node as well as in the bookkeeping. A stopped
// process keeps its unsealed keys in memory, so Pause never needs an unseal.
func Pause() Nemesis {
	return &fault{
		name: "pause",
		plan: staticPlan(signalStep(syscall.SIGSTOP)),
		heal: signalStep(syscall.SIGCONT),
	}
}
