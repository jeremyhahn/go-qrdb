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

// BuildBinary compiles pkg (for example DaemonPackage) from the repository
// root and returns the path to the resulting executable. The binary is built
// at most once per process for each root/package/tag combination.
//
// The output directory is an os.MkdirTemp directory rather than a t.TempDir()
// one on purpose: the cache outlives the test that first populated it, and a
// t.TempDir() would be removed as soon as that test finished, breaking every
// later caller.
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
	result := entry.(*buildResult)
	result.once.Do(func() {
		result.path, result.err = build(pkg, cfg)
	})
	return result.path, result.err
}

// build shells out to the Go toolchain once for a cache miss.
func build(pkg string, cfg buildConfig) (string, error) {
	dir, err := os.MkdirTemp("", "qrdbdharness-*")
	if err != nil {
		return "", &BuildError{Package: pkg, Tags: cfg.tags, Err: err}
	}
	binary := filepath.Join(dir, cfg.binaryName)
	cmd := exec.Command("go", "build", "-buildvcs=false", "-tags", cfg.tags, "-o", binary, pkg)
	cmd.Dir = cfg.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &BuildError{Package: pkg, Tags: cfg.tags, Output: string(out), Err: err}
	}
	return binary, nil
}

// RepoRoot returns the go-qrdb repository root, found by walking up from the
// working directory until a directory containing cmd/qrdbd is reached. This
// works from the root module, from a nested module such as
// cmd/qrdb-sqlite or sdk/go/gorm/conformance, and from any subdirectory.
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
	output io.Writer
	dir    string
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
	cmd *exec.Cmd

	waitOnce sync.Once
	waitErr  error
	done     chan struct{}
}

// Start launches binary with args and returns immediately; use the WaitFor
// helpers to block until the node is ready. The caller is responsible for
// calling Stop or Kill.
func Start(binary string, args []string, opts ...DaemonOption) (*Daemon, error) {
	var cfg daemonConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	cmd := exec.Command(binary, args...)
	cmd.Dir = cfg.dir
	if cfg.output != nil {
		cmd.Stdout = cfg.output
		cmd.Stderr = cfg.output
	}
	if err := cmd.Start(); err != nil {
		return nil, &DaemonStartError{Binary: binary, Err: err}
	}
	return &Daemon{cmd: cmd, done: make(chan struct{})}, nil
}

// Cmd exposes the underlying command, for callers that need the process ID or
// to send a signal the harness does not model.
func (d *Daemon) Cmd() *exec.Cmd { return d.cmd }

// Wait blocks until the daemon exits and returns its exit status. It is safe
// to call concurrently and repeatedly; every caller sees the same result.
func (d *Daemon) Wait() error {
	d.reap()
	<-d.done
	return d.waitErr
}

// reap starts the single goroutine allowed to call exec.Cmd.Wait.
func (d *Daemon) reap() {
	d.waitOnce.Do(func() {
		go func() {
			d.waitErr = d.cmd.Wait()
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

// Kill terminates the daemon immediately and waits for it to be reaped.
func (d *Daemon) Kill() error {
	if d.cmd.Process == nil {
		return nil
	}
	if err := d.cmd.Process.Kill(); err != nil && !isProcessDone(err) {
		return &DaemonSignalError{Signal: "kill", Err: err}
	}
	_ = d.Wait()
	return nil
}

// isProcessDone reports whether err means the process had already exited.
func isProcessDone(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
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
