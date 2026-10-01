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
	"os"
	"path/filepath"
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
	case "pwd":
		dir, err := os.Getwd()
		if err != nil {
			return 1
		}
		os.Stdout.WriteString(dir + "\n")
	case reservePortsHelperMode:
		// A second harness process, as `go test` runs one per package.
		ports, err := ReserveUDPPorts(reservePortsHelperCount)
		if err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
		for _, p := range ports {
			os.Stdout.WriteString(strconv.Itoa(p) + "\n")
		}
	default:
		return 2
	}
	return 0
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
