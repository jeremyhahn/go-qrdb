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

// Package main implements qrdb, the QRDB client CLI for interacting
// with a qrdbd server via REST, gRPC, Unix socket, or MCP transports.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// version, gitCommit, and buildDate are set at build time via ldflags:
//
//	go build -ldflags "-X main.version=... -X main.gitCommit=... -X main.buildDate=..."
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

// parseLogArgs extracts --log-level and --log-file values from os.Args
// before Cobra processes them. This allows the logger to be initialized
// prior to the GUI/CLI branch point.
func parseLogArgs() (string, string) {
	level := "info"
	file := ""
	for i := 1; i < len(os.Args); i++ {
		switch {
		case os.Args[i] == "--log-level" && i+1 < len(os.Args):
			level = os.Args[i+1]
			i++
		case os.Args[i] == "--log-file" && i+1 < len(os.Args):
			file = os.Args[i+1]
			i++
		case len(os.Args[i]) > len("--log-level=") && os.Args[i][:len("--log-level=")] == "--log-level=":
			level = os.Args[i][len("--log-level="):]
		case len(os.Args[i]) > len("--log-file=") && os.Args[i][:len("--log-file=")] == "--log-file=":
			file = os.Args[i][len("--log-file="):]
		}
	}
	return level, file
}

// isGUIOnly returns true when the only arguments are log flags,
// meaning the user intends to launch the GUI with debug logging.
func isGUIOnly() bool {
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--log-level" || arg == "--log-file":
			i++ // skip the value
			continue
		case len(arg) > len("--log-level=") && arg[:len("--log-level=")] == "--log-level=":
			continue
		case len(arg) > len("--log-file=") && arg[:len("--log-file=")] == "--log-file=":
			continue
		default:
			return false
		}
	}
	return true
}

// initLogger creates a logger from the parsed log level and file flags.
func initLogger(level, file string) (*logger.Logger, error) {
	if _, ok := validLogLevels[level]; !ok {
		return nil, &InvalidLogLevelError{Level: level}
	}

	cfg := logger.LogConfig{
		Level:     level,
		Format:    "console",
		Output:    "stdout",
		Component: "qrdb",
	}
	if file != "" {
		cfg.Output = "file"
		cfg.File = file
	}
	return logger.New(cfg)
}

// hardenProcess is secure.HardenProcess, indirected only so a test can observe
// run's handling of a refusal without making its own test binary
// non-dumpable, which cannot be undone.
var hardenProcess = secure.HardenProcess

// hardenAtStartup applies process inspection hardening, sending any warning
// (the opt-out, or a platform with no mechanism) to w as text.
//
// The warning goes to w — standard error — and not to the application
// logger, which writes to standard output: stdout is a CLI command's data, and
// on a platform with no mechanism the warning is logged on every invocation,
// where it would corrupt `qrdb kv get key > file`.
//
// The outcome is not recorded anywhere else: this binary is a client and a
// desktop shell with no compliance audit trail of its own, so the warning on
// standard error is the record. An embedded node it starts writes its own
// trail.
func hardenAtStartup(w io.Writer) error {
	_, err := hardenProcess(slog.New(slog.NewTextHandler(w, nil)))
	return err
}

func main() {
	os.Exit(run())
}

// run is main's body, split out so that os.Exit is confined to main and every
// deferred cleanup here still runs before the process exits. It returns the
// process exit code.
func run() int {
	// Parse log flags from os.Args before Cobra to initialize logging
	// early, including for the GUI path which bypasses Cobra entirely.
	level, file := parseLogArgs()
	log, err := initLogger(level, file)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Harden before either branch: the desktop GUI and the CLI both unseal
	// barriers and read passphrases, PINs and shares into protected memory,
	// and none of it may be readable by a debugger or a same-user process
	// (go-qrdb#554). A refusal is fatal, exactly as it is for qrdbd.
	if err := hardenAtStartup(os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// When invoked with no subcommands (only log flags allowed) and a
	// graphical display is available, attempt to launch the GUI. If the
	// binary was built without the desktop tag, launchGUI returns
	// GUIUnavailableError and we fall through to the CLI.
	if isGUIOnly() && hasDisplay() {
		if err := launchGUI(log, file); err != nil {
			var unavail *GUIUnavailableError
			if !errors.As(err, &unavail) {
				_, _ = fmt.Fprintln(os.Stderr, err)
				return 1
			}
			// GUI unavailable — fall through to CLI.
		} else {
			return 0
		}
	}

	rootCmd := newRootCmd()

	// Cancel the root command context on SIGINT/SIGTERM so long-running
	// commands (notably `qrdb watch`, whose loop selects on ctx.Done() for
	// every transport) stop promptly on Ctrl-C instead of relying on the
	// default OS signal disposition to kill the process mid-operation.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
