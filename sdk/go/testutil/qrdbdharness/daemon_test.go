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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperEnv names the environment variable that turns this test binary into
// the stand-in subprocess the Daemon tests drive. Re-executing the test binary
// keeps the tests hermetic: they need a real, signalable child process without
// depending on any particular tool being installed on the host.
const helperEnv = "QRDBDHARNESS_HELPER_MODE"

const (
	// orphanHelperMode exits while a grandchild keeps its stdout open.
	orphanHelperMode = "orphan"

	// grandchildPrefix starts the line on which orphanHelperMode reports its
	// grandchild's PID, so the test can kill it.
	grandchildPrefix = "grandchild "
)

// TestMain either plays the stand-in subprocess or runs the test suite.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(runHelper(mode))
	}
	os.Exit(m.Run())
}

// runHelper implements the stand-in subprocess behaviors.
func runHelper(mode string) int {
	switch mode {
	case "sleep":
		time.Sleep(5 * time.Minute)
	case "print":
		os.Stdout.WriteString("first\nsecond\n")
		os.Stderr.WriteString("problem\n")
	case "booting":
		os.Stdout.WriteString("booting\n")
		time.Sleep(5 * time.Minute)
	case orphanHelperMode:
		// Leave a grandchild holding this process's stdout, then exit: the
		// shape of a daemon whose descendant outlives it.
		child, err := helperCommand("sleep")
		if err != nil {
			return 1
		}
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			return 1
		}
		os.Stdout.WriteString(grandchildPrefix + strconv.Itoa(child.Process.Pid) + "\n")
	case "pwd":
		dir, err := os.Getwd()
		if err != nil {
			return 1
		}
		os.Stdout.WriteString(dir + "\n")
	case argvHelperMode:
		// The argv the harness actually launched the daemon with.
		os.Stdout.WriteString(strings.Join(os.Args[1:], "\n") + "\n")
	default:
		return 2
	}
	return 0
}

// helperCommand returns a command that re-executes this test binary as the
// helper subprocess named by mode. The binary is the one this process is
// running, as the kernel resolves it, never os.Args[0], which the parent
// chooses; it runs with no arguments and no shell, and the mode travels in
// the environment.
func helperCommand(mode string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(self) {
		return nil, errors.New("qrdbdharness: test binary path " + strconv.Quote(self) + " is not absolute")
	}
	// #nosec G204,G702 -- self is this process's own executable, resolved by the kernel (os.Executable) and checked absolute above; no argument is passed and no shell is involved. The only other input, mode, goes into the environment, never the command line.
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	return cmd, nil
}

func TestHelperCommandReexecsThisBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := helperCommand("print")
	if err != nil {
		t.Fatalf("helperCommand: %v", err)
	}
	if cmd.Path != self || len(cmd.Args) != 1 {
		t.Errorf("command = %q %v, want this test binary %q with no arguments", cmd.Path, cmd.Args[1:], self)
	}
	if !slices.Contains(cmd.Env, helperEnv+"=print") {
		t.Errorf("environment lacks the helper mode %s=print", helperEnv)
	}
	out, err := cmd.Output()
	if err != nil || string(out) != "first\nsecond\n" {
		t.Errorf("helper ran with output %q, err %v; want the print helper's", out, err)
	}
}

// startHelper launches this test binary in the given helper mode.
func startHelper(t *testing.T, mode string, opts ...DaemonOption) *Daemon {
	t.Helper()
	t.Setenv(helperEnv, mode)
	daemon, err := Start(os.Args[0], nil, opts...)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Kill() })
	return daemon
}

func TestRepoRootFindsRepository(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "cmd", "qrdbd"))
	if err != nil || !info.IsDir() {
		t.Fatalf("RepoRoot returned %q, which has no cmd/qrdbd directory", root)
	}
}

func TestRepoRootFromFailsWithoutMarker(t *testing.T) {
	start := t.TempDir()
	_, err := repoRootFrom(start)
	var rootErr *RepoRootError
	if !errors.As(err, &rootErr) {
		t.Fatalf("err = %v, want *RepoRootError", err)
	}
	if rootErr.Start != start {
		t.Errorf("Start = %q, want %q", rootErr.Start, start)
	}
	if !strings.Contains(err.Error(), start) {
		t.Errorf("error %q does not mention the starting directory", err)
	}
}

func TestRepoRootFromWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "qrdbd"), dirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	nested := filepath.Join(root, "sdk", "go", "testutil")
	if err := os.MkdirAll(nested, dirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	got, err := repoRootFrom(nested)
	if err != nil {
		t.Fatalf("repoRootFrom: %v", err)
	}
	if got != root {
		t.Errorf("repoRootFrom = %q, want %q", got, root)
	}
}

// newBuildableModule writes a dependency-free module that compiles quickly.
func newBuildableModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module harnessbuild\n\ngo 1.26\n",
		"main.go": "package main\n\nfunc main() {}\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), fileMode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// The module deliberately lives outside the repository workspace, so the
	// build must not try to resolve it through go.work.
	t.Setenv("GOWORK", "off")
	return dir
}

func TestBuildBinaryCompilesAndCaches(t *testing.T) {
	dir := newBuildableModule(t)

	first, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary: %v", err)
	}
	if filepath.Base(first) != "harnessbuild" {
		t.Errorf("binary name = %q, want harnessbuild", filepath.Base(first))
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("stat built binary: %v", err)
	}

	second, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary (cached): %v", err)
	}
	if second != first {
		t.Errorf("second build returned %q, want the cached %q", second, first)
	}
}

func TestBuildBinaryReportsCompilerOutput(t *testing.T) {
	dir := newBuildableModule(t)

	_, err := BuildBinary("./missing", WithRepoRoot(dir), WithBuildTags("harness_build_test"))
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("err = %v, want *BuildError", err)
	}
	if buildErr.Package != "./missing" {
		t.Errorf("Package = %q, want ./missing", buildErr.Package)
	}
	if buildErr.Output == "" {
		t.Error("BuildError carries no compiler output")
	}
	if buildErr.Unwrap() == nil {
		t.Error("BuildError does not wrap the exec failure")
	}

	// The failure is memoized like a success, so a retry is free rather than
	// re-running the toolchain.
	if _, again := BuildBinary("./missing", WithRepoRoot(dir), WithBuildTags("harness_build_test")); again != err {
		t.Errorf("second failure = %v, want the memoized %v", again, err)
	}
}

func TestBuildBinaryDefaultsToRepositoryRoot(t *testing.T) {
	// No WithRepoRoot: the build must run in the discovered repository root,
	// which is where a package path like ./cmd/qrdbd resolves. Naming a
	// package that cannot exist keeps this cheap — the toolchain fails on the
	// missing directory without compiling anything.
	_, err := BuildBinary("./cmd/qrdbd-not-a-real-package")
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("err = %v, want *BuildError", err)
	}
	root, rootErr := RepoRoot()
	if rootErr != nil {
		t.Fatalf("RepoRoot: %v", rootErr)
	}
	if !strings.Contains(buildErr.Output, root) {
		t.Errorf("compiler output %q does not mention the repository root %q", buildErr.Output, root)
	}
}

// harnessTempDirs lists the qrdbdharness-* build directories under root.
func harnessTempDirs(t *testing.T, root string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, "qrdbdharness-*"))
	if err != nil {
		t.Fatalf("glob build directories: %v", err)
	}
	return dirs
}

func TestRemoveBuildsDeletesEveryBuildDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := newBuildableModule(t)

	first, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary: %v", err)
	}
	second, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild2"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary (second name): %v", err)
	}
	if got := harnessTempDirs(t, tmp); len(got) != 2 {
		t.Fatalf("build directories before RemoveBuilds = %v, want 2", got)
	}

	if err := RemoveBuilds(); err != nil {
		t.Fatalf("RemoveBuilds: %v", err)
	}
	if got := harnessTempDirs(t, tmp); len(got) != 0 {
		t.Errorf("build directories after RemoveBuilds = %v, want none", got)
	}
	for _, binary := range []string{first, second} {
		if _, err := os.Stat(binary); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat %s after RemoveBuilds = %v, want not-exist", binary, err)
		}
	}
}

func TestRemoveBuildsForgetsTheCache(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := newBuildableModule(t)

	first, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary: %v", err)
	}
	if err := RemoveBuilds(); err != nil {
		t.Fatalf("RemoveBuilds: %v", err)
	}

	// A caller after RemoveBuilds gets a fresh, runnable build rather than
	// the cached path of a binary that no longer exists.
	again, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary after RemoveBuilds: %v", err)
	}
	t.Cleanup(func() { _ = RemoveBuilds() })
	if again == first {
		t.Errorf("BuildBinary after RemoveBuilds returned the removed path %q", first)
	}
	if _, err := os.Stat(again); err != nil {
		t.Errorf("stat rebuilt binary: %v", err)
	}
}

func TestRemoveBuildsWithNothingBuilt(t *testing.T) {
	if err := RemoveBuilds(); err != nil {
		t.Fatalf("RemoveBuilds (first): %v", err)
	}
	if err := RemoveBuilds(); err != nil {
		t.Errorf("RemoveBuilds on an empty cache = %v, want nil", err)
	}
}

func TestRemoveBuildsReportsADirectoryItCannotRemove(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := newBuildableModule(t)

	binary, err := BuildBinary(".", WithRepoRoot(dir), WithBinaryName("harnessbuild"), WithBuildTags("harness_build_test"))
	if err != nil {
		t.Fatalf("BuildBinary: %v", err)
	}
	// The removal fails through the remover rather than through permission
	// bits on the directory, which root, the uid CI runs as, overrides.
	buildDir := filepath.Dir(binary)
	removeErr := &os.PathError{Op: "unlinkat", Path: binary, Err: errors.New("device or resource busy")}
	var removed []string
	removeBuildDir = func(dir string) error {
		removed = append(removed, dir)
		return removeErr
	}
	t.Cleanup(func() {
		removeBuildDir = os.RemoveAll
		_ = os.RemoveAll(buildDir)
	})

	err = RemoveBuilds()
	if len(removed) != 1 || removed[0] != buildDir {
		t.Fatalf("RemoveBuilds removed %q, want only %q", removed, buildDir)
	}
	var cleanupErr *BuildCleanupError
	if !errors.As(err, &cleanupErr) {
		t.Fatalf("RemoveBuilds = %v, want *BuildCleanupError", err)
	}
	if cleanupErr.Dir != buildDir {
		t.Errorf("Dir = %q, want %q", cleanupErr.Dir, buildDir)
	}
	if !errors.Is(err, removeErr) {
		t.Errorf("RemoveBuilds = %v, want it to wrap %v", err, removeErr)
	}
	if !strings.Contains(cleanupErr.Error(), buildDir) {
		t.Errorf("Error() = %q, want it to name %s", cleanupErr.Error(), buildDir)
	}
}

func TestBuildBinaryFailureLeavesNoDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := newBuildableModule(t)

	if _, err := BuildBinary("./missing", WithRepoRoot(dir), WithBuildTags("harness_build_test")); err == nil {
		t.Fatal("BuildBinary of a missing package succeeded")
	}
	if got := harnessTempDirs(t, tmp); len(got) != 0 {
		t.Errorf("a failed build left %v behind", got)
	}
}

func TestBuildBinaryDefaults(t *testing.T) {
	cfg := buildConfig{tags: DefaultBuildTags, binaryName: filepath.Base(DaemonPackage)}
	if cfg.binaryName != "qrdbd" {
		t.Errorf("default binary name = %q, want qrdbd", cfg.binaryName)
	}
	if !strings.Contains(DefaultBuildTags, "raft") || !strings.Contains(DefaultBuildTags, "qrdb_mysql") {
		t.Errorf("DefaultBuildTags = %q, want the full daemon tag set", DefaultBuildTags)
	}
	if ClientPackage != "./cmd/qrdb" {
		t.Errorf("ClientPackage = %q, want ./cmd/qrdb", ClientPackage)
	}
}

func TestStartRejectsMissingBinary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-binary")
	_, err := Start(missing, []string{"start"})
	var startErr *DaemonStartError
	if !errors.As(err, &startErr) {
		t.Fatalf("err = %v, want *DaemonStartError", err)
	}
	if startErr.Binary != missing {
		t.Errorf("Binary = %q, want %q", startErr.Binary, missing)
	}
}

func TestDaemonKill(t *testing.T) {
	daemon := startHelper(t, "sleep")
	if daemon.Cmd().Process == nil {
		t.Fatal("Cmd().Process is nil for a started daemon")
	}
	if err := daemon.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := daemon.Wait(); err == nil {
		t.Error("Wait returned nil for a killed process")
	}
	// Killing an already reaped daemon is a no-op, not an error.
	if err := daemon.Kill(); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

func TestDaemonStopSignalsGracefully(t *testing.T) {
	daemon := startHelper(t, "sleep")
	start := time.Now()
	if err := daemon.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= stopGrace {
		t.Errorf("Stop took %v, want a graceful exit well under the %v grace period", elapsed, stopGrace)
	}
	if err := daemon.Wait(); err == nil {
		t.Error("Wait returned nil for an interrupted process")
	}
}

func TestDaemonStopAfterExit(t *testing.T) {
	daemon := startHelper(t, "print")
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := daemon.Stop(); err != nil {
		t.Errorf("Stop on an exited daemon: %v", err)
	}
}

func TestDaemonWaitIsIdempotent(t *testing.T) {
	daemon := startHelper(t, "print")
	first := daemon.Wait()
	second := daemon.Wait()
	if first != nil || second != nil {
		t.Fatalf("Wait = (%v, %v), want both nil", first, second)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := daemon.Wait(); err != nil {
				t.Errorf("concurrent Wait: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestDaemonOutputIsPrefixed(t *testing.T) {
	var buf bytes.Buffer
	daemon := startHelper(t, "print", WithOutput(NewPrefixWriter(&buf, "[node-1] ")))
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"[node-1] first\n", "[node-1] second\n", "[node-1] problem\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q does not contain %q", got, want)
		}
	}
}

func TestDaemonWorkingDir(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	daemon := startHelper(t, "pwd", WithWorkingDir(dir), WithOutput(&buf))
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// macOS reports temp directories through a symlink, so compare resolved paths.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != want && got != dir {
		t.Errorf("working directory = %q, want %q", got, want)
	}
}

// withWaitDelay overrides the exec.Cmd.WaitDelay Start sets, so a test can
// observe it without spending the production delay.
func withWaitDelay(d time.Duration) DaemonOption {
	return func(c *daemonConfig) { c.waitDelay = d }
}

// waitForTail polls d's output tail until it contains want or a deadline
// passes, and returns the tail.
func waitForTail(t *testing.T, d *Daemon, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		tail := d.Tail()
		if strings.Contains(tail, want) {
			return tail
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon output %q never contained %q", tail, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startOrphan starts the orphan helper and registers a cleanup that kills the
// grandchild it leaves behind.
func startOrphan(t *testing.T, opts ...DaemonOption) *Daemon {
	t.Helper()
	daemon := startHelper(t, orphanHelperMode, opts...)
	tail := waitForTail(t, daemon, "\n")
	line := strings.TrimSpace(strings.TrimPrefix(tail, grandchildPrefix))
	pid, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("orphan helper output %q carries no grandchild PID: %v", tail, err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	return daemon
}

func TestDaemonTailWithoutOutputOption(t *testing.T) {
	daemon := startHelper(t, "print")
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got := daemon.Tail()
	for _, want := range []string{"first\n", "second\n", "problem\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("Tail() = %q, want it to contain %q", got, want)
		}
	}
}

func TestDaemonTailAlongsideOutputOption(t *testing.T) {
	var buf bytes.Buffer
	daemon := startHelper(t, "print", WithOutput(&buf))
	if err := daemon.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(buf.String(), "second\n") {
		t.Errorf("WithOutput writer got %q, want the daemon's output", buf.String())
	}
	if !strings.Contains(daemon.Tail(), "second\n") {
		t.Errorf("Tail() = %q, want the daemon's output as well", daemon.Tail())
	}
}

func TestDaemonWaitContextReturnsExitStatus(t *testing.T) {
	daemon := startHelper(t, "print")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := daemon.WaitContext(ctx); err != nil {
		t.Fatalf("WaitContext: %v", err)
	}
	if err := daemon.WaitContext(ctx); err != nil {
		t.Errorf("second WaitContext: %v", err)
	}
}

func TestDaemonWaitContextExpiresWithTail(t *testing.T) {
	daemon := startHelper(t, "booting")
	waitForTail(t, daemon, "booting\n")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := daemon.WaitContext(ctx)
	var waitErr *DaemonWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("err = %v, want *DaemonWaitError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if waitErr.PID != daemon.Cmd().Process.Pid {
		t.Errorf("PID = %d, want %d", waitErr.PID, daemon.Cmd().Process.Pid)
	}
	if !strings.Contains(err.Error(), "booting") {
		t.Errorf("error %q does not carry the daemon's output tail", err)
	}
	// The daemon is still running: a timed-out wait does not kill it.
	if err := daemon.Kill(); err != nil {
		t.Fatalf("Kill after an expired wait: %v", err)
	}
}

func TestDaemonWaitDelayBoundsLingeringOutput(t *testing.T) {
	daemon := startOrphan(t, withWaitDelay(100*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The helper exited at once, but its grandchild still holds the output
	// pipe; without WaitDelay this wait would last as long as the grandchild.
	err := daemon.WaitContext(ctx)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("WaitContext = %v, want exec.ErrWaitDelay", err)
	}
}

func TestDaemonKillReportsUnreapedDaemon(t *testing.T) {
	daemon := startOrphan(t, withWaitDelay(time.Minute))
	err := daemon.kill(50 * time.Millisecond)
	var waitErr *DaemonWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("kill = %v, want *DaemonWaitError", err)
	}
	if !strings.Contains(waitErr.Tail, grandchildPrefix) {
		t.Errorf("Tail = %q, want the daemon's output", waitErr.Tail)
	}
}

func TestTailWriterKeepsTheLastBytes(t *testing.T) {
	w := newTailWriter(8)
	for _, s := range []string{"abc", "defg", "hijk"} {
		if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", s, n, err, len(s))
		}
	}
	if got, want := w.String(), "defghijk"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestTailWriterOversizedWrite(t *testing.T) {
	w := newTailWriter(4)
	if _, err := w.Write([]byte("xy")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n, err := w.Write([]byte("0123456789")); n != 10 || err != nil {
		t.Fatalf("Write = (%d, %v), want (10, nil)", n, err)
	}
	if got, want := w.String(), "6789"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestTailWriterIsConcurrencySafe(t *testing.T) {
	w := newTailWriter(64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = w.Write([]byte("line\n"))
				_ = w.String()
			}
		}()
	}
	wg.Wait()
	if got := len(w.String()); got != 64 {
		t.Errorf("len(String()) = %d, want the 64-byte limit", got)
	}
}

func TestPrefixWriter(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{name: "single line", writes: []string{"hello\n"}, want: "> hello\n"},
		{name: "two lines in one write", writes: []string{"a\nb\n"}, want: "> a\n> b\n"},
		{name: "line split across writes", writes: []string{"he", "llo", "\n"}, want: "> hello\n"},
		{name: "partial line is buffered", writes: []string{"no newline yet"}, want: ""},
		{name: "empty line", writes: []string{"\n"}, want: "> \n"},
		{name: "nothing written", writes: nil, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewPrefixWriter(&buf, "> ")
			for _, chunk := range tc.writes {
				n, err := w.Write([]byte(chunk))
				if err != nil {
					t.Fatalf("Write: %v", err)
				}
				if n != len(chunk) {
					t.Errorf("Write returned %d, want %d", n, len(chunk))
				}
			}
			if buf.String() != tc.want {
				t.Errorf("output = %q, want %q", buf.String(), tc.want)
			}
		})
	}
}

// failingWriter fails every write, to exercise PrefixWriter's error path.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestPrefixWriterPropagatesWriteErrors(t *testing.T) {
	sentinel := errors.New("closed")
	w := NewPrefixWriter(failingWriter{err: sentinel}, "> ")
	n, err := w.Write([]byte("line\n"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if n != len("line\n") {
		t.Errorf("n = %d, want %d", n, len("line\n"))
	}
}

func TestPrefixWriterIsConcurrencySafe(t *testing.T) {
	var buf bytes.Buffer
	w := NewPrefixWriter(&buf, "> ")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				if _, err := w.Write([]byte("line\n")); err != nil {
					t.Errorf("Write: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	if got := strings.Count(buf.String(), "> line\n"); got != 8*32 {
		t.Errorf("emitted %d prefixed lines, want %d", got, 8*32)
	}
}
