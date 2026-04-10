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
	"errors"
	"fmt"
	"os"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
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

func main() {
	// Parse log flags from os.Args before Cobra to initialize logging
	// early, including for the GUI path which bypasses Cobra entirely.
	level, file := parseLogArgs()
	log, err := initLogger(level, file)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
				os.Exit(1)
			}
			// GUI unavailable — fall through to CLI.
		} else {
			return
		}
	}

	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
