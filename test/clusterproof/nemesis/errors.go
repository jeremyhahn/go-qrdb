//go:build clusterproof

package nemesis

import "errors"

var (
	// ErrUnsupported reports that a target cannot perform the primitive a
	// fault needs. A target that returns it was not touched, so a fault never
	// records it as needing a heal.
	ErrUnsupported = errors.New("nemesis: unsupported by target")

	// ErrUnhealed reports an Inject on a nemesis whose previous fault has not
	// been healed yet.
	ErrUnhealed = errors.New("nemesis: previous fault not healed")

	// ErrNoTargets reports an Inject with no targets.
	ErrNoTargets = errors.New("nemesis: no targets")

	// ErrNotRunning reports a signal to a local node whose process is not
	// running.
	ErrNotRunning = errors.New("nemesis: process not running")
)

// ConfigError reports an invalid nemesis, target or schedule configuration.
type ConfigError struct {
	Field  string
	Reason string
	Err    error
}

func (e *ConfigError) Error() string {
	msg := "nemesis: invalid " + e.Field + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ConfigError) Unwrap() error { return e.Err }

// FaultError reports a failed fault step on one node.
type FaultError struct {
	Nemesis string
	Op      string
	Node    string
	Err     error
}

func (e *FaultError) Error() string {
	return "nemesis: " + e.Nemesis + " " + e.Op + " on " + e.Node + ": " + e.Err.Error()
}

func (e *FaultError) Unwrap() error { return e.Err }

// CommandError reports a command that failed, with its combined output.
type CommandError struct {
	Argv   []string
	Output []byte
	Err    error
}

func (e *CommandError) Error() string {
	msg := "nemesis: command " + shellQuote(e.Argv) + ": " + e.Err.Error()
	if len(e.Output) > 0 {
		msg += ": " + string(e.Output)
	}
	return msg
}

func (e *CommandError) Unwrap() error { return e.Err }

// RefusedError reports a target whose clock-rate subcommand refused to
// change its clock. Reason is the refusal its exit status reports
// (clockrate.ErrNoSysTime, ErrHarnessKernel or ErrUsage) and Err the command
// failure; errors.Is matches either.
type RefusedError struct {
	Reason error
	Err    error
}

func (e *RefusedError) Error() string {
	return "nemesis: refused: " + e.Reason.Error() + ": " + e.Err.Error()
}

func (e *RefusedError) Unwrap() []error { return []error{e.Reason, e.Err} }
