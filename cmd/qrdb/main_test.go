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

//go:build !integration

package main

import (
	"bytes"
	"errors"
	"io"
	stdlog "log"
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// parseLogArgs
// ---------------------------------------------------------------------------

func TestParseLogArgs_Defaults(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb"}
	level, file := parseLogArgs()
	if level != "info" {
		t.Fatalf("expected default level %q, got %q", "info", level)
	}
	if file != "" {
		t.Fatalf("expected empty file, got %q", file)
	}
}

func TestParseLogArgs_SpaceSeparatedLogLevel(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level", "debug"}
	level, file := parseLogArgs()
	if level != "debug" {
		t.Fatalf("expected level %q, got %q", "debug", level)
	}
	if file != "" {
		t.Fatalf("expected empty file, got %q", file)
	}
}

func TestParseLogArgs_EqualsSeparatedLogLevel(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level=warn"}
	level, file := parseLogArgs()
	if level != "warn" {
		t.Fatalf("expected level %q, got %q", "warn", level)
	}
	if file != "" {
		t.Fatalf("expected empty file, got %q", file)
	}
}

func TestParseLogArgs_SpaceSeparatedLogFile(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-file", "/tmp/qrdb.log"}
	level, file := parseLogArgs()
	if level != "info" {
		t.Fatalf("expected default level %q, got %q", "info", level)
	}
	if file != "/tmp/qrdb.log" {
		t.Fatalf("expected file %q, got %q", "/tmp/qrdb.log", file)
	}
}

func TestParseLogArgs_EqualsSeparatedLogFile(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-file=/var/log/qrdb.log"}
	level, file := parseLogArgs()
	if level != "info" {
		t.Fatalf("expected default level %q, got %q", "info", level)
	}
	if file != "/var/log/qrdb.log" {
		t.Fatalf("expected file %q, got %q", "/var/log/qrdb.log", file)
	}
}

func TestParseLogArgs_BothFlags(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level", "error", "--log-file", "/tmp/test.log"}
	level, file := parseLogArgs()
	if level != "error" {
		t.Fatalf("expected level %q, got %q", "error", level)
	}
	if file != "/tmp/test.log" {
		t.Fatalf("expected file %q, got %q", "/tmp/test.log", file)
	}
}

func TestParseLogArgs_BothFlagsEqualsForm(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level=trace", "--log-file=/tmp/trace.log"}
	level, file := parseLogArgs()
	if level != "trace" {
		t.Fatalf("expected level %q, got %q", "trace", level)
	}
	if file != "/tmp/trace.log" {
		t.Fatalf("expected file %q, got %q", "/tmp/trace.log", file)
	}
}

func TestParseLogArgs_InterspersedWithOtherArgs(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "kv", "put", "--log-level", "debug", "foo", "bar"}
	level, file := parseLogArgs()
	if level != "debug" {
		t.Fatalf("expected level %q, got %q", "debug", level)
	}
	if file != "" {
		t.Fatalf("expected empty file, got %q", file)
	}
}

func TestParseLogArgs_LogLevelAtEndWithoutValue(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	// --log-level at end without a following value should not change default
	os.Args = []string{"qrdb", "--log-level"}
	level, file := parseLogArgs()
	if level != "info" {
		t.Fatalf("expected default level %q, got %q", "info", level)
	}
	if file != "" {
		t.Fatalf("expected empty file, got %q", file)
	}
}

func TestParseLogArgs_LogFileAtEndWithoutValue(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	// --log-file at end without a following value should not change default
	os.Args = []string{"qrdb", "--log-file"}
	level, file := parseLogArgs()
	if level != "info" {
		t.Fatalf("expected default level %q, got %q", "info", level)
	}
	if file != "" {
		t.Fatalf("expected empty file, got %q", file)
	}
}

// ---------------------------------------------------------------------------
// isGUIOnly
// ---------------------------------------------------------------------------

func TestIsGUIOnly_NoArgs(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb"}
	if !isGUIOnly() {
		t.Fatal("expected true when no arguments are provided")
	}
}

func TestIsGUIOnly_OnlyLogLevel(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level", "debug"}
	if !isGUIOnly() {
		t.Fatal("expected true when only --log-level is provided")
	}
}

func TestIsGUIOnly_OnlyLogLevelEquals(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level=debug"}
	if !isGUIOnly() {
		t.Fatal("expected true when only --log-level= is provided")
	}
}

func TestIsGUIOnly_OnlyLogFile(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-file", "/tmp/qrdb.log"}
	if !isGUIOnly() {
		t.Fatal("expected true when only --log-file is provided")
	}
}

func TestIsGUIOnly_OnlyLogFileEquals(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-file=/tmp/qrdb.log"}
	if !isGUIOnly() {
		t.Fatal("expected true when only --log-file= is provided")
	}
}

func TestIsGUIOnly_BothLogFlags(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level", "warn", "--log-file", "/tmp/test.log"}
	if !isGUIOnly() {
		t.Fatal("expected true when only log flags are provided")
	}
}

func TestIsGUIOnly_WithSubcommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "kv", "get", "foo"}
	if isGUIOnly() {
		t.Fatal("expected false when subcommand arguments are present")
	}
}

func TestIsGUIOnly_LogFlagsWithSubcommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--log-level", "debug", "kv", "get", "foo"}
	if isGUIOnly() {
		t.Fatal("expected false when subcommand follows log flags")
	}
}

func TestIsGUIOnly_VersionFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--version"}
	if isGUIOnly() {
		t.Fatal("expected false when --version flag is present")
	}
}

func TestIsGUIOnly_HelpFlag(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	os.Args = []string{"qrdb", "--help"}
	if isGUIOnly() {
		t.Fatal("expected false when --help flag is present")
	}
}

// ---------------------------------------------------------------------------
// initLogger
// ---------------------------------------------------------------------------

func TestInitLogger_ValidLevel(t *testing.T) {
	log, err := initLogger("info", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestInitLogger_AllValidLevels(t *testing.T) {
	levels := []string{"trace", "debug", "info", "warn", "error"}
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			log, err := initLogger(level, "")
			if err != nil {
				t.Fatalf("unexpected error for level %q: %v", level, err)
			}
			if log == nil {
				t.Fatalf("expected non-nil logger for level %q", level)
			}
		})
	}
}

func TestInitLogger_InvalidLevel(t *testing.T) {
	_, err := initLogger("verbose", "")
	if err == nil {
		t.Fatal("expected error for invalid log level")
	}
	var target *InvalidLogLevelError
	if !errors.As(err, &target) {
		t.Fatalf("expected *InvalidLogLevelError, got %T: %v", err, err)
	}
	if target.Level != "verbose" {
		t.Fatalf("expected Level %q, got %q", "verbose", target.Level)
	}
}

func TestInitLogger_InvalidLevel_CaseSensitive(t *testing.T) {
	_, err := initLogger("INFO", "")
	if err == nil {
		t.Fatal("expected error for uppercase log level")
	}
	var target *InvalidLogLevelError
	if !errors.As(err, &target) {
		t.Fatalf("expected *InvalidLogLevelError, got %T: %v", err, err)
	}
}

func TestInitLogger_WithLogFile(t *testing.T) {
	tmpFile := t.TempDir() + "/test.log"
	log, err := initLogger("debug", tmpFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestInitLogger_WithEmptyFile(t *testing.T) {
	log, err := initLogger("info", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log == nil {
		t.Fatal("expected non-nil logger with empty file path")
	}
}

// ---------------------------------------------------------------------------
// routeStdLog
// ---------------------------------------------------------------------------

type recordingDebugLogger struct{ msgs []string }

func (r *recordingDebugLogger) Debug(msg string, _ ...any) { r.msgs = append(r.msgs, msg) }

func TestRouteStdLog_ForwardsLibraryOutputToDebug(t *testing.T) {
	rec := &recordingDebugLogger{}
	restore := routeStdLog(rec)
	t.Cleanup(restore)

	stdlog.Print("failed to sufficiently increase receive buffer size\n")

	if len(rec.msgs) != 1 || rec.msgs[0] != "failed to sufficiently increase receive buffer size" {
		t.Fatalf("expected one trimmed debug message, got %q", rec.msgs)
	}
}

func TestRouteStdLog_LeavesStderrUntouched(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	restore := routeStdLog(&recordingDebugLogger{})
	stdlog.Print("quic-go noise")
	restore()
	_ = w.Close()

	data, _ := io.ReadAll(r)
	if len(data) != 0 {
		t.Fatalf("library log output reached stderr: %q", data)
	}
}

func TestRouteStdLog_RestoreReinstatesPreviousOutput(t *testing.T) {
	var buf bytes.Buffer
	stdlog.SetOutput(&buf)
	t.Cleanup(func() { stdlog.SetOutput(os.Stderr) })

	restore := routeStdLog(&recordingDebugLogger{})
	restore()
	stdlog.Print("after restore")

	if !bytes.Contains(buf.Bytes(), []byte("after restore")) {
		t.Fatalf("restore did not reinstate the previous writer, buffer: %q", buf.String())
	}
}
