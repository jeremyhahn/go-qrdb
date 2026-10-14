//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// Op is the kind of a nemesis event.
type Op string

const (
	// OpSchedule records the schedule a run plays, seed included.
	OpSchedule Op = "schedule"
	// OpInject records a window's fault being injected.
	OpInject Op = "inject"
	// OpHeal records a window's fault being healed.
	OpHeal Op = "heal"
)

// Phase is where in its operation an event was recorded.
type Phase string

const (
	PhaseInvoke Phase = "invoke"
	PhaseOK     Phase = "ok"
	PhaseFail   Phase = "fail"
)

// Event is one nemesis record for the run's history. It is deliberately
// small and self-describing, so the soak harness can carry it into the
// shared history beside the workload's operations.
type Event struct {
	Time     time.Time `json:"time"`
	Op       Op        `json:"op"`
	Phase    Phase     `json:"phase"`
	Window   int       `json:"window"`
	Nemesis  string    `json:"nemesis,omitempty"`
	Targets  []string  `json:"targets,omitempty"`
	Schedule *Schedule `json:"schedule,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Recorder receives a run's events in order.
type Recorder interface {
	Record(Event)
}

// RecorderFunc adapts a function to [Recorder].
type RecorderFunc func(Event)

// Record calls f.
func (f RecorderFunc) Record(e Event) { f(e) }

// Clock is the time source of a [Player].
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// DefaultHealTimeout bounds each heal a [Player] runs.
const DefaultHealTimeout = time.Minute

// Player plays a schedule: it injects each window's fault, holds it for the
// window's length, heals it, and records every step.
type Player struct {
	// Nemeses maps a window's nemesis name to the fault.
	Nemeses map[string]Nemesis
	// Targets maps a window's node names to targets.
	Targets map[string]Target
	// Recorder receives the events; it is required, because a run with no
	// record proves nothing.
	Recorder Recorder
	// Clock is the time source; nil is the system clock.
	Clock Clock
	// HealTimeout bounds each heal; zero is DefaultHealTimeout.
	HealTimeout time.Duration
}

// Play runs s. Every name in it is resolved before the first fault, and a
// fault is healed however its window ends: elapsed, failed to inject, or
// cancelled. A heal runs on a context detached from ctx, so a cancelled run
// still heals. Play stops at the first failed inject or heal, because a
// fault that did not happen makes the run vacuous and one that did not heal
// invalidates everything after it.
func (p *Player) Play(ctx context.Context, s Schedule) error {
	if p.Recorder == nil {
		return &ConfigError{Field: "player recorder", Reason: "is required"}
	}
	if err := s.Validate(); err != nil {
		return err
	}
	type resolved struct {
		nemesis Nemesis
		targets []Target
	}
	plan := make([]resolved, len(s.Windows))
	for i, w := range s.Windows {
		n, ok := p.Nemeses[w.Nemesis]
		if !ok {
			return &ConfigError{Field: "schedule window " + strconv.Itoa(i), Reason: "unknown nemesis " + strconv.Quote(w.Nemesis)}
		}
		plan[i].nemesis = n
		for _, name := range w.Targets {
			t, ok := p.Targets[name]
			if !ok {
				return &ConfigError{Field: "schedule window " + strconv.Itoa(i), Reason: "unknown target " + strconv.Quote(name)}
			}
			plan[i].targets = append(plan[i].targets, t)
		}
	}
	clock := p.Clock
	if clock == nil {
		clock = systemClock{}
	}
	healTimeout := p.HealTimeout
	if healTimeout <= 0 {
		healTimeout = DefaultHealTimeout
	}
	record := func(e Event) {
		e.Time = clock.Now()
		p.Recorder.Record(e)
	}
	record(Event{Op: OpSchedule, Phase: PhaseOK, Window: -1, Schedule: &s})

	begin := clock.Now()
	for i, w := range s.Windows {
		if err := sleepUntil(ctx, clock, begin.Add(w.Start)); err != nil {
			return err
		}
		n := plan[i].nemesis
		ev := Event{Window: i, Nemesis: w.Nemesis, Targets: w.Targets}

		ev.Op = OpInject
		record(withPhase(ev, PhaseInvoke, nil))
		injectErr := n.Inject(ctx, plan[i].targets)
		record(withPhase(ev, phaseOf(injectErr), injectErr))
		var waitErr error
		if injectErr == nil {
			waitErr = sleepUntil(ctx, clock, begin.Add(w.Start+w.Length))
		}

		ev.Op = OpHeal
		record(withPhase(ev, PhaseInvoke, nil))
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), healTimeout)
		healErr := n.Heal(hctx)
		cancel()
		record(withPhase(ev, phaseOf(healErr), healErr))

		if err := errors.Join(injectErr, healErr, waitErr); err != nil {
			return err
		}
	}
	return nil
}

// sleepUntil waits for the clock to reach t or ctx to end.
func sleepUntil(ctx context.Context, clock Clock, t time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d := t.Sub(clock.Now())
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-clock.After(d):
		return nil
	}
}

func phaseOf(err error) Phase {
	if err != nil {
		return PhaseFail
	}
	return PhaseOK
}

func withPhase(e Event, phase Phase, err error) Event {
	e.Phase = phase
	if err != nil {
		e.Error = err.Error()
	}
	return e
}
