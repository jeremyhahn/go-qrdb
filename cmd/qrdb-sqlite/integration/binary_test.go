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

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// qrdb-sqlite binary build — once per test binary run
//
// The qrdb-sqlite gateway is never exercised as a subprocess anywhere else in
// this module's test suite: every other integration test links the vtab and
// adapter packages directly into the test binary. This file closes that gap
// by building the real CGo binary (mattn/go-sqlite3 requires CGO_ENABLED=1)
// and driving it exactly as an operator would from a shell.
//
// Mirrors qrdbdharness.BuildBinary: sync.Once + os.MkdirTemp rather than
// t.TempDir(), because sync.Once only ever captures the first caller's
// *testing.T and a t.TempDir() directory is removed when that first test
// completes, which would break every subsequent test in this file. The build
// itself stays local rather than moving to the harness because it compiles
// this CGo module (CGO_ENABLED=1, mattn/go-sqlite3), not qrdbd.
// ---------------------------------------------------------------------------

var (
	buildSQLiteOnce   sync.Once
	builtSQLiteBinary string
	buildSQLiteErr    error
)

// buildQRDBSQLite compiles the qrdb-sqlite gateway binary into a temporary
// directory and returns its path. The binary is built once per test run,
// using the same build tag and CGO requirement as the module's own
// build-sqlite-gateway Makefile target.
func buildQRDBSQLite(t *testing.T) string {
	t.Helper()
	buildSQLiteOnce.Do(doBuildQRDBSQLite)
	require.NoError(t, buildSQLiteErr, "build qrdb-sqlite binary")
	return builtSQLiteBinary
}

// doBuildQRDBSQLite performs the one-shot CGO build. It is invoked through
// buildSQLiteOnce from either buildQRDBSQLite (per-test) or TestMain's
// pre-warm (ensureQRDBSQLiteBuilt). Pre-warming in TestMain matters: the
// CGO+race build can take tens of seconds on a loaded machine, and the
// per-test contexts are bounded (30s); if the first test triggered the build
// inside its own timeout window, that command's deadline would be consumed by
// build time and expire before the binary even ran.
func doBuildQRDBSQLite() {
	dir, err := os.MkdirTemp("", "qrdb-sqlite-bin-test-*")
	if err != nil {
		buildSQLiteErr = fmt.Errorf("MkdirTemp: %w", err)
		return
	}
	builtSQLiteBinary = filepath.Join(dir, "qrdb-sqlite")

	cmd := exec.Command("go", "build", "-buildvcs=false",
		"-tags", "sqlite_vtable",
		"-o", builtSQLiteBinary, ".")
	cmd.Dir = qrdbSQLiteModuleRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")

	out, err := cmd.CombinedOutput()
	if err != nil {
		buildSQLiteErr = fmt.Errorf("go build (qrdb-sqlite) failed: %w\n%s", err, out)
	}
}

// ensureQRDBSQLiteBuilt pre-warms the binary build from TestMain so no
// per-test timeout ever includes build time. Returns the build error, if any.
func ensureQRDBSQLiteBuilt() error {
	buildSQLiteOnce.Do(doBuildQRDBSQLite)
	return buildSQLiteErr
}

// qrdbSQLiteModuleRoot walks up from the current working directory to find
// the go.mod that declares module github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite.
// The qrdb-sqlite binary lives in its own Go module (separate from the
// top-level go-qrdb module) because it requires CGo via mattn/go-sqlite3.
func qrdbSQLiteModuleRoot() string {
	dir, _ := os.Getwd()
	for {
		modFile := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(modFile); err == nil {
			if strings.Contains(string(data), "module github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite\n") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fallback: the integration package's parent directory is the module root.
	dir, _ = os.Getwd()
	return filepath.Dir(dir)
}

// runQRDBSQLite runs the built qrdb-sqlite binary with args, feeding stdin (if
// non-empty) to the process and returning combined stdout+stderr, the process
// exit code, and any error starting/waiting on the process that is not an
// *exec.ExitError (i.e. a genuine harness failure, not a nonzero exit).
func runQRDBSQLite(t *testing.T, ctx context.Context, stdin string, args ...string) (output string, exitCode int) {
	t.Helper()
	bin := buildQRDBSQLite(t)

	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("run qrdb-sqlite %v: %v\noutput:\n%s", args, err, out)
	return "", -1
}

// asExitError reports whether err is an *exec.ExitError, assigning it to
// target when so. It exists only to keep runQRDBSQLite free of a direct
// "errors" import for this single type assertion.
func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// ---------------------------------------------------------------------------
// version — no server required
// ---------------------------------------------------------------------------

func TestBinary_Version_ExitsZeroWithoutServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, code := runQRDBSQLite(t, ctx, "", "version")
	require.Equal(t, 0, code, "exit code; output: %s", out)
	require.Contains(t, out, "qrdb-sqlite version")
}

func TestBinary_Version_DoesNotRequireFlags(t *testing.T) {
	// version must succeed with zero flags and zero stdin — it takes the
	// PersistentPreRunE short-circuit before any connection setup.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, code := runQRDBSQLite(t, ctx, "", "version")
	require.Equal(t, 0, code)
	require.Contains(t, out, "commit")
	require.Contains(t, out, "built")
}

// ---------------------------------------------------------------------------
// --help — lists both subcommands
// ---------------------------------------------------------------------------

func TestBinary_Help_ListsBothSubcommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, code := runQRDBSQLite(t, ctx, "", "--help")
	require.Equal(t, 0, code, "output: %s", out)
	require.Contains(t, out, "shell")
	require.Contains(t, out, "version")
	require.Contains(t, out, "Available Commands")
}

// ---------------------------------------------------------------------------
// Flag validation errors — typed *ConfigError messages from config.go
// ---------------------------------------------------------------------------

func TestBinary_FlagValidation_Errors(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "invalid mode",
			args:       []string{"shell", "--mode=badmode"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for mode: must be one of: embedded, client",
		},
		{
			name:       "invalid transport",
			args:       []string{"shell", "--mode=client", "--server=localhost:63099", "--transport=bogus"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for transport: must be one of: rest, grpc, unix, mcp, quic",
		},
		{
			name:       "embedded mode without --config",
			args:       []string{"shell", "--mode=embedded"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for config: required when mode is embedded",
		},
		{
			name:       "client mode without --server",
			args:       []string{"shell", "--mode=client"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for server: required when mode is client",
		},
		{
			name:       "invalid consistency",
			args:       []string{"shell", "--mode=client", "--server=localhost:63099", "--consistency=bogus"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for consistency: must be one of: linearizable, serializable, bounded-staleness, eventual",
		},
		{
			name:       "invalid write-mode",
			args:       []string{"shell", "--mode=client", "--server=localhost:63099", "--write-mode=bogus"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for write-mode: must be one of: sync, wal_durable, batched",
		},
		{
			name:       "non-positive chunk-size",
			args:       []string{"shell", "--mode=client", "--server=localhost:63099", "--chunk-size=0"},
			wantExit:   1,
			wantStderr: "qrdb-sqlite: invalid configuration for chunk-size: must be a positive integer",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// No stdin: these fail during PersistentPreRunE, before the shell
			// REPL ever reads from stdin.
			out, code := runQRDBSQLite(t, ctx, "", tc.args...)
			require.Equal(t, tc.wantExit, code, "output: %s", out)
			require.Contains(t, out, tc.wantStderr)
		})
	}
}

// ---------------------------------------------------------------------------
// shell — real subprocess round-trip against the shared qrdbd server
// ---------------------------------------------------------------------------

// unixTLSArgs returns the qrdb-sqlite CLI flags needed to authenticate over
// the Unix-socket transport against the shared qrdbd server. Unix
// (grpc-over-UDS) is "verified TLS 1.3 only", identical to gRPC/REST/QUIC/
// MCP: the CLI must present the shared admin client certificate and trust
// the shared CA exactly as it would for any other transport.
func unixTLSArgs() []string {
	return []string{
		"--transport=unix",
		"--server=" + shared.unixSocket,
		"--ca-file=" + shared.ca.CertPath,
		"--cert-file=" + shared.clientCertPath,
		"--key-file=" + shared.clientKeyPath,
	}
}

// TestBinary_Shell_UnixTransport_KVRoundTrip drives the qrdb-sqlite binary as
// a real subprocess, in client mode over the Unix-socket transport
// (verified TLS 1.3 with the shared admin identity), against the shared
// qrdbd server started by TestMain. SQL is piped over stdin exactly as an
// interactive operator would type it, exercising discovery, virtual-table
// registration, INSERT, SELECT, and the .quit dot-command end to end.
func TestBinary_Shell_UnixTransport_KVRoundTrip(t *testing.T) {
	if shared == nil {
		t.Fatal("shared server is nil — TestMain did not start it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const (
		key = "binary-subprocess-key"
		val = "binary-subprocess-value"
	)
	stdin := fmt.Sprintf(
		"INSERT INTO kv (key, value) VALUES ('%s', '%s');\nSELECT key, value FROM kv WHERE key = '%s';\n.quit\n",
		key, val, key,
	)

	args := append([]string{"shell", "--mode=client"}, unixTLSArgs()...)
	out, code := runQRDBSQLite(t, ctx, stdin, args...)
	require.Equal(t, 0, code, "shell subprocess exit code; output:\n%s", out)
	require.Contains(t, out, key, "output should echo the inserted key:\n%s", out)
	require.Contains(t, out, val, "output should echo the inserted value:\n%s", out)
}

// TestBinary_Shell_UnixTransport_TablesDotCommand verifies that the
// .tables dot-command, run through the real subprocess REPL, lists the
// auto-discovered "kv" table once the shell has connected to the live
// server and run discovery.
func TestBinary_Shell_UnixTransport_TablesDotCommand(t *testing.T) {
	if shared == nil {
		t.Fatal("shared server is nil — TestMain did not start it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := append([]string{"shell", "--mode=client"}, unixTLSArgs()...)
	out, code := runQRDBSQLite(t, ctx, ".tables\n.quit\n", args...)
	require.Equal(t, 0, code, "shell subprocess exit code; output:\n%s", out)
	require.Contains(t, out, "kv", ".tables output should list the discovered kv table:\n%s", out)
}

// TestBinary_Shell_UnixTransport_QRDBStatusPragma verifies that the custom
// ".pragma qrdb_status" dot-command, run through the real subprocess REPL
// against the shared qrdbd server, prints the live session settings instead
// of silently producing no output (real SQLite has no knowledge of qrdb_*
// pragma names and used to no-op on them before the shell intercepted them).
func TestBinary_Shell_UnixTransport_QRDBStatusPragma(t *testing.T) {
	if shared == nil {
		t.Fatal("shared server is nil — TestMain did not start it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := append([]string{"shell", "--mode=client"}, unixTLSArgs()...)
	out, code := runQRDBSQLite(t, ctx, ".pragma qrdb_status\n.quit\n", args...)
	require.Equal(t, 0, code, "shell subprocess exit code; output:\n%s", out)
	require.Contains(t, out, "consistency", "qrdb_status output should include the consistency setting:\n%s", out)
	require.Contains(t, out, "write_mode", "qrdb_status output should include the write_mode setting:\n%s", out)
	require.Contains(t, out, "client", "qrdb_status output should report client mode:\n%s", out)
}

// TestBinary_Shell_UnixTransport_QRDBWriteModePragma verifies that
// ".pragma qrdb_write_mode wal_durable" actually changes the session's write
// mode (round-tripped through .pragma qrdb_status), rather than being a
// silent no-op against the real SQLite engine.
func TestBinary_Shell_UnixTransport_QRDBWriteModePragma(t *testing.T) {
	if shared == nil {
		t.Fatal("shared server is nil — TestMain did not start it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := append([]string{"shell", "--mode=client"}, unixTLSArgs()...)
	stdin := ".pragma qrdb_write_mode wal_durable\n.pragma qrdb_status\n.quit\n"
	out, code := runQRDBSQLite(t, ctx, stdin, args...)
	require.Equal(t, 0, code, "shell subprocess exit code; output:\n%s", out)
	require.Contains(t, out, "wal_durable", "qrdb_status output should reflect the new write mode:\n%s", out)
}

// TestBinary_Shell_ClientMode_UnreachableServer_ExitsNonZero drives the
// binary against a guaranteed-unreachable gRPC address (port 1 is reserved
// and always refuses) so that buildClient's Connect call fails inside
// PersistentPreRunE, exercising the shell subcommand's *ConnectionError /
// *TransportError surface as a real process exit, not just an in-process
// unit test.
func TestBinary_Shell_ClientMode_UnreachableServer_ExitsNonZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, code := runQRDBSQLite(t, ctx, "",
		"shell",
		"--mode=client",
		"--transport=grpc",
		"--server=127.0.0.1:1",
	)
	require.NotEqual(t, 0, code, "output: %s", out)
	require.Contains(t, out, "qrdb-sqlite:", "error should be prefixed by the CLI's own error wrapper:\n%s", out)
}
