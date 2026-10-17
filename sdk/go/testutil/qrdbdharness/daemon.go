// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package qrdbdharness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DaemonPackage is the import path of the qrdbd daemon, relative to the
	// repository root.
	DaemonPackage = "./cmd/qrdbd"

	// ClientPackage is the import path of the qrdb client CLI, relative to the
	// repository root.
	ClientPackage = "./cmd/qrdb"

	// DefaultBuildTags is the tag set qrdbd must be built with for a harnessed
	// node to serve every protocol the integration suites exercise.
	DefaultBuildTags = "codec_json raft qrdb_ts qrdb_vector qrdb_sql qrdb_mysql qrdb_pgsql"

	// stopGrace is how long Stop waits for a signalled daemon to exit before
	// killing it.
	stopGrace = 10 * time.Second

	// killGrace is how long Kill waits for a killed daemon to be reaped. A
	// SIGKILLed process is gone within milliseconds; one still unreaped after
	// this is stuck in the kernel, and the caller is told so rather than
	// blocked until the test binary's own timeout.
	killGrace = 10 * time.Second

	// waitDelay bounds how long exec.Cmd.Wait keeps copying the daemon's
	// output after the daemon has exited. Without it a grandchild holding the
	// inherited stdout or stderr open blocks every Wait forever.
	waitDelay = 5 * time.Second

	// tailSize is how many trailing bytes of a daemon's output are kept for
	// DaemonWaitError and Tail, enough for the last few dozen log lines.
	tailSize = 16 << 10
)

// buildConfig holds the resolved BuildOption values.
type buildConfig struct {
	tags       string
	root       string
	binaryName string
}

// BuildOption customizes a BuildBinary invocation.
type BuildOption func(*buildConfig)

// WithBuildTags overrides the build tags the binary is compiled with.
func WithBuildTags(tags string) BuildOption {
	return func(c *buildConfig) { c.tags = tags }
}

// WithRepoRoot overrides the directory `go build` runs in. It defaults to the
// repository root discovered by RepoRoot.
func WithRepoRoot(root string) BuildOption {
	return func(c *buildConfig) { c.root = root }
}

// WithBinaryName overrides the compiled binary's file name, which defaults to
// the last element of the package path.
func WithBinaryName(name string) BuildOption {
	return func(c *buildConfig) {
		if name != "" {
			c.binaryName = name
		}
	}
}

// buildResult memoizes one compiled binary.
type buildResult struct {
	once sync.Once
	path string
	err  error
}

// builds caches build results by root + package + tags, so a test binary that
// needs both qrdbd and the qrdb CLI compiles each exactly once, and repeated
// calls for the same target are free.
var builds sync.Map

// removeBuildDir removes one build directory for RemoveBuilds. It is a
// variable so a test can make the removal fail for every uid: permission bits
// do not stop root, which is the uid CI runs as.
var removeBuildDir = os.RemoveAll

// BuildBinary compiles pkg (for example DaemonPackage) from the repository
// root and returns the path to the resulting executable. The binary is built
// at most once per process for each root/package/tag combination.
//
// The output directory is an os.MkdirTemp directory rather than a t.TempDir()
// one on purpose: the cache outlives the test that first populated it, and a
// t.TempDir() would be removed as soon as that test finished, breaking every
// later caller. RemoveBuilds removes them once the process is done with them.
func BuildBinary(pkg string, opts ...BuildOption) (string, error) {
	cfg := buildConfig{tags: DefaultBuildTags, binaryName: path.Base(pkg)}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.root == "" {
		root, err := RepoRoot()
		if err != nil {
			return "", err
		}
		cfg.root = root
	}

	key := cfg.root + "\x00" + pkg + "\x00" + cfg.tags + "\x00" + cfg.binaryName
	entry, _ := builds.LoadOrStore(key, &buildResult{})
	result, ok := entry.(*buildResult)
	if !ok {
		// Unreachable: `builds` is written only by the LoadOrStore above, whose
		// value is always a *buildResult. The assertion is checked rather than
		// bare because .golangci.yml turns on errcheck's check-type-assertions,
		// and it falls back to an uncached build rather than declaring an error
		// no code path can return -- the defect #243 is about. A rebuild is a
		// better failure than a panic in a harness shared by every consumer.
		result = &buildResult{}
	}
	result.once.Do(func() {
		result.path, result.err = build(pkg, cfg)
	})
	return result.path, result.err
}

// RemoveBuilds removes the directory of every binary BuildBinary compiled in
// this process and empties the cache, so a later BuildBinary compiles afresh.
// The cache outlives every test, so nothing else removes those directories:
// a test binary calls it from TestMain once m.Run has returned, and a program
// before it exits. Each binary is a full race-built qrdbd or qrdb, and a
// process that skips this leaves them in os.TempDir. A directory that cannot
// be removed is reported as a *BuildCleanupError; the rest are still removed.
func RemoveBuilds() error {
	var errs []error
	builds.Range(func(key, entry any) bool {
		builds.Delete(key)
		result, ok := entry.(*buildResult)
		if !ok || result.path == "" {
			return true
		}
		dir := filepath.Dir(result.path)
		if err := removeBuildDir(dir); err != nil {
			errs = append(errs, &BuildCleanupError{Dir: dir, Err: err})
		}
		return true
	})
	return errors.Join(errs...)
}

// build shells out to the Go toolchain once for a cache miss.
func build(pkg string, cfg buildConfig) (string, error) {
	dir, err := os.MkdirTemp("", "qrdbdharness-*")
	if err != nil {
		return "", &BuildError{Package: pkg, Tags: cfg.tags, Err: err}
	}
	binary := filepath.Join(dir, cfg.binaryName)
	// #nosec G204 -- the program is the literal "go"; every variable argument
	// is the test's own. cfg.tags comes from DefaultBuildTags or a BuildOption
	// the test passed, pkg is the package path the test named, and binary is a
	// path under this package's own MkdirTemp. No request value reaches any of
	// them, and qrdbdharness is a test harness that only ever runs from a test
	// binary.
	//
	// The sibling call in Start carries the same annotation. Both were invisible
	// until #293 put sdk/go under gosec at all.
	cmd := exec.Command("go", "build", "-buildvcs=false", "-tags", cfg.tags, "-o", binary, pkg)
	cmd.Dir = cfg.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A failed build has no binary for RemoveBuilds to find, so its
		// directory goes now. The removal error is dropped: the build failure
		// is the one the caller needs.
		_ = os.RemoveAll(dir)
		return "", &BuildError{Package: pkg, Tags: cfg.tags, Output: string(out), Err: err}
	}
	return binary, nil
}

// RepoRoot returns the go-qrdb repository root, found by walking up from the
// working directory until a directory containing cmd/qrdbd is reached. This
// works from the root module, from a nested module such as
// sdk/go/gorm/conformance, and from any subdirectory.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", &RepoRootError{Err: err}
	}
	return repoRootFrom(dir)
}

// repoRootFrom walks up from start looking for the repository marker.
func repoRootFrom(start string) (string, error) {
	dir := start
	for {
		if info, err := os.Stat(filepath.Join(dir, "cmd", "qrdbd")); err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", &RepoRootError{Start: start}
		}
		dir = parent
	}
}

// daemonConfig holds the resolved DaemonOption values.
type daemonConfig struct {
	output    io.Writer
	dir       string
	waitDelay time.Duration
}

// DaemonOption customizes a Start invocation.
type DaemonOption func(*daemonConfig)

// WithOutput sends the daemon's stdout and stderr to w. Wrap w in a
// PrefixWriter to tag each line with the node it came from.
func WithOutput(w io.Writer) DaemonOption {
	return func(c *daemonConfig) { c.output = w }
}

// WithWorkingDir sets the daemon subprocess's working directory.
func WithWorkingDir(dir string) DaemonOption {
	return func(c *daemonConfig) { c.dir = dir }
}

// Daemon is a running qrdbd subprocess.
type Daemon struct {
	cmd  *exec.Cmd
	tail *tailWriter

	// args and out let dumpStacks find the daemon serving a port and wait
	// for its goroutine dump to be written.
	args []string
	out  *outputWriter

	waitOnce sync.Once
	waitErr  error
	done     chan struct{}
}

// Start launches binary with args and returns immediately; use the WaitFor
// helpers to block until the node is ready. The caller is responsible for
// calling Stop or Kill. A `start` argv first passes through ReserveListenerPorts, so the
// daemon binds no fixed default port the test left unset.
//
// The daemon's output always goes to a bounded in-memory tail as well as to
// any WithOutput writer, so a wait that times out can report what the daemon
// said last. The command's WaitDelay is set, so even a caller that drives the
// returned Cmd itself never blocks in Wait on output a lingering descendant
// holds open.
func Start(binary string, args []string, opts ...DaemonOption) (*Daemon, error) {
	cfg := daemonConfig{waitDelay: waitDelay}
	for _, opt := range opts {
		opt(&cfg)
	}
	args, err := ReserveListenerPorts(args)
	if err != nil {
		return nil, err
	}

	tail := newTailWriter(tailSize)
	var sink io.Writer = tail
	if cfg.output != nil {
		sink = io.MultiWriter(tail, cfg.output)
	}
	// #nosec G204,G702 -- qrdbdharness is a test harness: `binary` is the path
	// this package's own Build produced from a package path the test named, and
	// `args` are the test's. Neither is reachable from a network peer. Both
	// rules fire on the same call -- G204 on the subprocess, G702 on the taint
	// path into it -- so both are named; suppressing one leaves the other red.
	cmd := exec.Command(binary, args...)
	cmd.Dir = cfg.dir
	out := newOutputWriter(sink)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = cfg.waitDelay
	if err := cmd.Start(); err != nil {
		return nil, &DaemonStartError{Binary: binary, Err: err}
	}
	d := &Daemon{cmd: cmd, tail: tail, args: args, out: out, done: make(chan struct{})}
	spawned.Store(d, struct{}{})
	return d, nil
}

// Cmd exposes the underlying command, for callers that need the process ID or
// to send a signal the harness does not model.
func (d *Daemon) Cmd() *exec.Cmd { return d.cmd }

// Tail returns the last bytes the daemon wrote to stdout and stderr.
func (d *Daemon) Tail() string { return d.tail.String() }

// Wait blocks until the daemon exits and returns its exit status. It is safe
// to call concurrently and repeatedly; every caller sees the same result. It
// waits as long as the daemon runs: a caller that has not already made the
// daemon exit should use WaitContext.
func (d *Daemon) Wait() error {
	d.reap()
	<-d.done
	return d.waitErr
}

// WaitContext is Wait bounded by ctx. If ctx ends first it returns a
// *DaemonWaitError carrying the daemon's output tail; the daemon is left
// running for the caller to Kill.
func (d *Daemon) WaitContext(ctx context.Context) error {
	d.reap()
	select {
	case <-d.done:
		return d.waitErr
	case <-ctx.Done():
		return d.waitError(ctx.Err())
	}
}

// waitError describes a wait that ended before the daemon was reaped.
func (d *Daemon) waitError(err error) *DaemonWaitError {
	return &DaemonWaitError{PID: d.cmd.Process.Pid, Err: err, Tail: d.Tail()}
}

// reap starts the single goroutine allowed to call exec.Cmd.Wait.
func (d *Daemon) reap() {
	d.waitOnce.Do(func() {
		go func() {
			d.waitErr = d.cmd.Wait()
			spawned.Delete(d)
			close(d.done)
		}()
	})
}

// Stop asks the daemon to shut down gracefully, escalating to a kill if it has
// not exited within the grace period, and waits for it to be reaped. The
// daemon's own exit status is not reported — a signalled process exits
// non-zero by definition; call Wait afterwards if that status matters.
func (d *Daemon) Stop() error {
	if d.cmd.Process == nil {
		return nil
	}
	if err := d.cmd.Process.Signal(os.Interrupt); err != nil {
		if isProcessDone(err) {
			_ = d.Wait()
			return nil
		}
		return &DaemonSignalError{Signal: "interrupt", Err: err}
	}
	d.reap()
	select {
	case <-d.done:
		return nil
	case <-time.After(stopGrace):
		return d.Kill()
	}
}

// Kill terminates the daemon immediately and waits up to killGrace for it to
// be reaped, returning a *DaemonWaitError if it is not.
func (d *Daemon) Kill() error {
	return d.kill(killGrace)
}

// kill is Kill with the reap bound as a parameter, so tests need not spend
// the production grace.
func (d *Daemon) kill(grace time.Duration) error {
	if d.cmd.Process == nil {
		return nil
	}
	if err := d.cmd.Process.Kill(); err != nil && !isProcessDone(err) {
		return &DaemonSignalError{Signal: "kill", Err: err}
	}
	d.reap()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-d.done:
		return nil
	case <-timer.C:
		return d.waitError(context.DeadlineExceeded)
	}
}

// isProcessDone reports whether err means the process had already exited.
func isProcessDone(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
}

// tailWriter keeps the last limit bytes written to it. It is safe for
// concurrent use, because a subprocess's stdout and stderr are copied from
// different goroutines.
type tailWriter struct {
	limit int

	mu  sync.Mutex
	buf []byte
}

// newTailWriter returns a tailWriter that keeps the last limit bytes.
func newTailWriter(limit int) *tailWriter {
	return &tailWriter{limit: limit}
}

// Write appends p, discarding the oldest bytes beyond the limit.
func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) >= w.limit {
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return len(p), nil
	}
	if over := len(w.buf) + len(p) - w.limit; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// String returns the retained bytes.
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// PrefixWriter forwards complete lines to an underlying writer, tagging each
// one with a prefix so several daemons can share a log stream. Partial writes
// are buffered until their newline arrives. It is safe for concurrent use,
// which matters because a subprocess's stdout and stderr are written from
// different goroutines.
type PrefixWriter struct {
	w      io.Writer
	prefix string

	mu  sync.Mutex
	buf []byte
}

// NewPrefixWriter returns a PrefixWriter that writes prefixed lines to w.
func NewPrefixWriter(w io.Writer, prefix string) *PrefixWriter {
	return &PrefixWriter{w: w, prefix: prefix}
}

// Write buffers p and emits every complete line it contains.
func (w *PrefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := w.buf[:idx]
		w.buf = w.buf[idx+1:]
		if _, err := w.w.Write([]byte(w.prefix + string(line) + "\n")); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}
