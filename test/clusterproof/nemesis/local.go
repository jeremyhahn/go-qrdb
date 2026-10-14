//go:build clusterproof

package nemesis

import (
	"context"
	"os"
	"syscall"
)

// LocalConfig configures a [LocalProcess].
type LocalConfig struct {
	// Node is the node's name.
	Node string
	// Process returns the server process the harness started for the node,
	// or nil while it is not running. It is called on every signal, so it
	// follows the process across restarts.
	Process func() *os.Process
	// Restart starts the node's server process if it is not running and is a
	// no-op if it is (see [Restarter]).
	Restart func(ctx context.Context) error
}

// LocalProcess is the [Target] for CI and single-host runs: a server process
// the harness started on this host. It only signals that process; it runs no
// commands, so the faults that need [Target.Exec] (partition, disk, clock)
// are refused with ErrUnsupported and never reach the host.
type LocalProcess struct {
	node    string
	process func() *os.Process
	restart func(ctx context.Context) error
}

var _ Restarter = (*LocalProcess)(nil)

// NewLocalProcess returns the target for one harness-started process.
func NewLocalProcess(cfg LocalConfig) (*LocalProcess, error) {
	if cfg.Node == "" {
		return nil, &ConfigError{Field: "local target node", Reason: "must not be empty"}
	}
	if cfg.Process == nil {
		return nil, &ConfigError{Field: "local target " + cfg.Node, Reason: "process accessor is required"}
	}
	if cfg.Restart == nil {
		return nil, &ConfigError{Field: "local target " + cfg.Node, Reason: "restart function is required"}
	}
	return &LocalProcess{node: cfg.Node, process: cfg.Process, restart: cfg.Restart}, nil
}

// Name returns the node's name.
func (l *LocalProcess) Name() string { return l.node }

// Exec is unsupported: a local target never runs commands on the host.
func (l *LocalProcess) Exec(context.Context, []string) ([]byte, error) {
	return nil, ErrUnsupported
}

// Signal sends sig to the node's current process.
func (l *LocalProcess) Signal(ctx context.Context, sig syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := l.process()
	if p == nil {
		return ErrNotRunning
	}
	return p.Signal(sig)
}

// Restart brings the node's process back through the harness.
func (l *LocalProcess) Restart(ctx context.Context) error { return l.restart(ctx) }
