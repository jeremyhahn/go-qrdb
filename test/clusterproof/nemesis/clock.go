//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/clockrate"
)

// DefaultClockRateCommand runs the proof binary's clock-rate subcommand on
// the node's kernel, with ArgvElement expanding to the subcommand's flags.
// The subcommand sets the kernel's tick and frequency through adjtimex(2),
// which governs CLOCK_MONOTONIC, the clock Go's timers and the Raft lease run
// on, as well as CLOCK_REALTIME. adjtimex(2) is not namespaced: it needs
// CAP_SYS_TIME and changes the rate of the whole kernel, so each replica a
// run slows needs a kernel of its own (a host or a VM), and replicas sharing
// a kernel drift together and test nothing. A time-sync daemon on that
// kernel steers the rate back; a proof host runs without one.
var DefaultClockRateCommand = []string{"clusterproof", "clock-rate", ArgvElement}

// ClockRateConfig configures the clock-rate fault.
type ClockRateConfig struct {
	// PPM is the rate change in parts per million: -5000 makes a target's
	// clocks run 0.5% slow, 20000 makes them run 2% fast. It must be
	// non-zero and within ±100000, the tick's range.
	PPM int
	// Command is the template that runs `clusterproof clock-rate` on a
	// node's own kernel, rendered with .Node and with ArgvElement, which it
	// must contain, expanding to the subcommand's flags; nil is
	// DefaultClockRateCommand.
	Command []string
	// Harness is the boot ID of the kernel the harness runs on. The
	// subcommand refuses to change that kernel, so a target that shares it
	// is refused rather than slowing the harness's own clocks.
	Harness string
}

type clockRateData struct {
	Node string
}

// NewClockRate returns the fault that changes each target's clock rate by
// PPM. A wall-clock step would not do: the Raft lease is measured on the
// leader's monotonic clock, which a step leaves alone, so only a rate
// difference between replicas can stretch a lease past the moment the other
// replicas stop honouring it.
//
// Before touching any target, Inject runs the subcommand's check on every
// one; a target without CAP_SYS_TIME, or on the harness's kernel, fails the
// Inject with a *RefusedError and no clock is changed. Its heal restores the
// nominal rate rather than undoing PPM, so healing twice cannot drift the
// clock the other way.
func NewClockRate(cfg ClockRateConfig) (Nemesis, error) {
	if _, err := clockrate.ForPPM(cfg.PPM); cfg.PPM == 0 || err != nil {
		return nil, &ConfigError{Field: "clock-rate ppm", Reason: "must be non-zero and within ±100000", Err: err}
	}
	if cfg.Harness == "" {
		return nil, &ConfigError{Field: "clock-rate harness", Reason: "the harness kernel's boot ID is required"}
	}
	cmd, err := NewTemplate("clock-rate command", orDefault(cfg.Command, DefaultClockRateCommand))
	if err != nil {
		return nil, err
	}
	if !cmd.takesArgv() {
		return nil, &ConfigError{Field: "clock-rate command", Reason: "must contain " + ArgvElement + " for the subcommand's flags"}
	}
	if _, err := cmd.Render(clockRateData{Node: "node"}, nil); err != nil {
		return nil, err
	}
	run := func(ctx context.Context, t Target, flags ...string) error {
		argv, err := cmd.Render(clockRateData{Node: t.Name()}, append([]string{"-not-kernel", cfg.Harness}, flags...))
		if err != nil {
			return err
		}
		_, err = t.Exec(ctx, argv)
		return refusal(err)
	}
	setRate := func(ppm int) step {
		return func(ctx context.Context, t Target) error {
			return run(ctx, t, "-ppm", strconv.Itoa(ppm))
		}
	}
	plan := func(ctx context.Context, targets []Target) (step, error) {
		for _, t := range targets {
			if err := run(ctx, t, "-check"); err != nil {
				return nil, &FaultError{Nemesis: "clock-rate", Op: "check", Node: t.Name(), Err: err}
			}
		}
		return setRate(cfg.PPM), nil
	}
	return &fault{name: "clock-rate", plan: plan, heal: setRate(0)}, nil
}

// refusal returns err with the refusal its exit status reports, if the
// clock-rate subcommand refused: an exec template carries a remote command's
// status but not its error.
func refusal(err error) error {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		return err
	}
	if reason := clockrate.Refusal(exit.ExitCode()); reason != nil {
		return &RefusedError{Reason: reason, Err: err}
	}
	return err
}
