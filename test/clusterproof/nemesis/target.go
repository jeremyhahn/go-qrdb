//go:build clusterproof

package nemesis

import (
	"context"
	"os/exec"
	"syscall"
)

// Target is one node a fault acts on. A fault reaches the node only through
// a Target, never the local host directly.
type Target interface {
	// Name is the node's name, as the schedule and the history spell it.
	Name() string
	// Exec runs argv on the node and returns its combined output. A target
	// that cannot run commands returns an error wrapping ErrUnsupported.
	Exec(ctx context.Context, argv []string) ([]byte, error)
	// Signal sends sig to the node's server process.
	Signal(ctx context.Context, sig syscall.Signal) error
}

// Restarter is a Target whose server process can be brought back after it was
// killed. Restart starts the process if it is not running and is a no-op if
// it is, so a heal can call it without knowing whether the kill landed.
type Restarter interface {
	Target
	Restart(ctx context.Context) error
}

// Unsealer is a Restarter whose node comes back sealed: a barrier that holds
// its key only in memory refuses every read and write after a restart until
// the node's own credential unseals it. Unseal unseals the node and is a
// no-op if it is already unsealed, so a heal can retry it.
type Unsealer interface {
	Restarter
	Unseal(ctx context.Context) error
}

// Runner runs a command on the host that drives the faults: the transport
// seam of [RemoteTarget]. The argv is already rendered: ssh, the container
// runtime, or whatever the deployment uses, is in it.
type Runner interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// CommandRunner is the [Runner] that executes argv as a child process.
type CommandRunner struct{}

var _ Runner = CommandRunner{}

// Run executes argv and returns its combined output.
func (CommandRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, &ConfigError{Field: "argv", Reason: "command is empty"}
	}
	// Running the operator's configured command is this type's purpose; it
	// takes an argv, never a shell string, and the faults validate every
	// value they put in one.
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput() //nolint:gosec // G204: see the note above
	if err != nil {
		return out, &CommandError{Argv: argv, Output: out, Err: err}
	}
	return out, nil
}

// signalNames spells each signal a fault sends the way kill(1) and
// `docker kill --signal` take it.
var signalNames = map[syscall.Signal]string{
	syscall.SIGKILL: "KILL",
	syscall.SIGSTOP: "STOP",
	syscall.SIGCONT: "CONT",
	syscall.SIGTERM: "TERM",
}

func signalName(sig syscall.Signal) (string, error) {
	name, ok := signalNames[sig]
	if !ok {
		return "", &ConfigError{Field: "signal", Reason: "no name for signal " + sig.String()}
	}
	return name, nil
}
