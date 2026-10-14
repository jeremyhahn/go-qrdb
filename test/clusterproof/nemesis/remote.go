//go:build clusterproof

package nemesis

import (
	"context"
	"syscall"
)

// RemoteConfig configures a [RemoteTarget]. Each command is a [Template]
// rendered against the node's parameters: .Node, .Vars (the free-form map
// below, for the host, the container name and so on), .Argv (the fault's
// command, for Exec) and .Signal (the signal's name without "SIG", for
// Signal).
//
// Whether a node runs the server as a binary or a container is a deployment
// choice this type does not make: the templates are the seam. A container
// deployment over ssh might use
//
//	Exec:    ssh {{.Vars.host}} -- docker run --rm --privileged --net=container:{{.Vars.container}} {{.Vars.faultImage}} {{shquote .Argv}}
//	Signal:  ssh {{.Vars.host}} -- docker kill --signal={{.Signal}} {{.Vars.container}}
//	Restart: ssh {{.Vars.host}} -- docker start {{.Vars.container}}
//	Unseal:  ssh {{.Vars.host}} -- {{.Vars.unsealCommand}}
//
// one argv element per word.
type RemoteConfig struct {
	Node string
	Vars map[string]string
	// Exec wraps a fault's argv; it normally carries ArgvElement or
	// {{shquote .Argv}}.
	Exec []string
	// Signal signals the node's server process.
	Signal []string
	// Restart starts the node's server process if it is not running and is
	// a no-op if it is (see [Restarter]).
	Restart []string
	// Unseal unseals the node with its own credential after a restart and
	// is a no-op if it is already unsealed (see [Unsealer]). It is optional:
	// a node whose seal opens itself at boot needs none. The credential stays
	// in the operator's command; the harness never holds it.
	Unseal []string
	// Runner runs the rendered commands; nil is [CommandRunner].
	Runner Runner
}

// RemoteTarget is the [Target] for a node on another host, reached through
// command templates run by a [Runner].
type RemoteTarget struct {
	node    string
	vars    map[string]string
	exec    *Template
	signal  *Template
	restart *Template
	unseal  *Template
	runner  Runner
}

var _ Unsealer = (*RemoteTarget)(nil)

// remoteData is what a RemoteTarget's templates render against.
type remoteData struct {
	Node   string
	Vars   map[string]string
	Argv   []string
	Signal string
}

// NewRemoteTarget parses the templates and renders each once against sample
// data, so a typo in a template or a missing variable fails here rather than
// in the middle of a heal.
func NewRemoteTarget(cfg RemoteConfig) (*RemoteTarget, error) {
	if cfg.Node == "" {
		return nil, &ConfigError{Field: "remote target node", Reason: "must not be empty"}
	}
	r := &RemoteTarget{node: cfg.Node, vars: cfg.Vars, runner: cfg.Runner}
	if r.vars == nil {
		r.vars = map[string]string{}
	}
	if r.runner == nil {
		r.runner = CommandRunner{}
	}
	for _, f := range []struct {
		dst      **Template
		name     string
		argv     []string
		optional bool
	}{
		{&r.exec, "exec", cfg.Exec, false},
		{&r.signal, "signal", cfg.Signal, false},
		{&r.restart, "restart", cfg.Restart, false},
		{&r.unseal, "unseal", cfg.Unseal, true},
	} {
		if f.optional && len(f.argv) == 0 {
			continue
		}
		t, err := NewTemplate("remote target "+cfg.Node+" "+f.name, f.argv)
		if err != nil {
			return nil, err
		}
		if _, err := t.Render(r.data([]string{"true"}, "KILL"), []string{"true"}); err != nil {
			return nil, err
		}
		*f.dst = t
	}
	return r, nil
}

func (r *RemoteTarget) data(argv []string, signal string) remoteData {
	return remoteData{Node: r.node, Vars: r.vars, Argv: argv, Signal: signal}
}

// Name returns the node's name.
func (r *RemoteTarget) Name() string { return r.node }

// Exec runs argv on the node through the Exec template.
func (r *RemoteTarget) Exec(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, &ConfigError{Field: "argv", Reason: "command is empty"}
	}
	return r.run(ctx, r.exec, r.data(argv, ""), argv)
}

// Signal signals the node's server process through the Signal template.
func (r *RemoteTarget) Signal(ctx context.Context, sig syscall.Signal) error {
	name, err := signalName(sig)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, r.signal, r.data(nil, name), nil)
	return err
}

// Restart brings the node's server process back through the Restart
// template.
func (r *RemoteTarget) Restart(ctx context.Context) error {
	_, err := r.run(ctx, r.restart, r.data(nil, ""), nil)
	return err
}

// Unseal unseals the node through the Unseal template. A target configured
// without one returns an error wrapping [ErrUnsupported].
func (r *RemoteTarget) Unseal(ctx context.Context) error {
	if r.unseal == nil {
		return &ConfigError{Field: "remote target " + r.node + " unseal", Reason: "no unseal command is configured", Err: ErrUnsupported}
	}
	_, err := r.run(ctx, r.unseal, r.data(nil, ""), nil)
	return err
}

func (r *RemoteTarget) run(ctx context.Context, t *Template, data remoteData, inner []string) ([]byte, error) {
	argv, err := t.Render(data, inner)
	if err != nil {
		return nil, err
	}
	return r.runner.Run(ctx, argv)
}
