// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// database_remote_mode_removed_cli_test.go proves, through the real client
// CLI on every transport, that remote-mode databases are gone (OD-9 A): the
// CLI has no update-connection verb and `database info` reports no
// connection block.
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDatabase_CLI_UpdateConnectionRemoved_PerProtocol asserts, on every
// transport, that `qrdb database update-connection` is not a command and that
// a database's info carries no connection block.
func TestDatabase_CLI_UpdateConnectionRemoved_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			dbName := fmt.Sprintf("noremote-%s-%d", proto.name, time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
			})
			if out, err := runCLIProto(t, node, proto, "database", "create", dbName, "--backend", "pebble"); err != nil {
				t.Fatalf("[%s] database create %s: %v\noutput: %s", proto.name, dbName, err, out)
			}

			out, err := runCLIProto(t, node, proto, "database", "update-connection", dbName)
			// cobra answers an unknown sub-command of a non-runnable group with
			// the group's help; the verb must neither run nor be listed.
			if err != nil && !strings.Contains(out+err.Error(), `unknown command "update-connection"`) {
				t.Fatalf("[%s] database update-connection: want unknown command or the group help, got %v\noutput: %s", proto.name, err, out)
			}
			if err == nil && !strings.Contains(out, "Available Commands:") {
				t.Fatalf("[%s] database update-connection ran instead of printing the group help\noutput: %s", proto.name, out)
			}
			if strings.Contains(out, "update-connection") {
				t.Fatalf("[%s] database update-connection is still a command:\n%s", proto.name, out)
			}

			help, err := runCLIProto(t, node, proto, "database", "--help")
			if err != nil {
				t.Fatalf("[%s] database --help: %v\noutput: %s", proto.name, err, help)
			}
			if strings.Contains(help, "update-connection") {
				t.Fatalf("[%s] database --help still lists update-connection:\n%s", proto.name, help)
			}

			raw, err := runCLIProto(t, node, proto, "--output", "json", "database", "info", dbName)
			if err != nil {
				t.Fatalf("[%s] database info %s: %v\noutput: %s", proto.name, dbName, err, raw)
			}
			if strings.Contains(strings.ToLower(raw), `"connection"`) {
				t.Fatalf("[%s] database info %s carries a connection block:\n%s", proto.name, dbName, raw)
			}
		})
	}
}

// TestDatabase_CLI_RemoteModeRefused_PerProtocol asserts remote mode cannot
// be asked for at create or at config (OD-9 A). The CLI has no create flag
// that names a mode or a connection, so on every transport such a create is
// refused by the CLI and no database is made; and a node whose config file
// carries a `databases[]` entry with a `connection:` block refuses to start
// with the strict decode's unknown-key error.
func TestDatabase_CLI_RemoteModeRefused_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			dbName := fmt.Sprintf("remotemode-%s-%d", proto.name, time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
			})
			for _, flags := range [][]string{
				{"--mode", "remote"},
				{"--connection-address", "127.0.0.1:1"},
			} {
				args := append([]string{"database", "create", dbName, "--backend", "pebble"}, flags...)
				out, err := runCLIProto(t, node, proto, args...)
				if err == nil {
					t.Fatalf("[%s] database create %v: want an unknown flag, it succeeded\noutput: %s", proto.name, flags, out)
				}
				if !strings.Contains(out+err.Error(), "unknown flag: "+flags[0]) {
					t.Fatalf("[%s] database create %v: want unknown flag %s, got %v\noutput: %s", proto.name, flags, flags[0], err, out)
				}
			}
			if out, err := runCLIProto(t, node, proto, "database", "info", dbName); err == nil {
				t.Fatalf("[%s] a refused remote create made database %s:\n%s", proto.name, dbName, out)
			}
		})
	}

	t.Run("config", func(t *testing.T) {
		bin := buildBinary(t)
		extra := "databases:\n  - name: \"app\"\n    backend: \"pebble\"\n    connection:\n      transport: \"tcp\"\n      address: \"127.0.0.1:1\"\n"
		cfgPath := writeStandaloneModeConfig(t, "pebble", extra)
		dataDir := t.TempDir()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "start", "--config", cfgPath, "--mode", "standalone", "--data-dir", dataDir)
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("qrdbd started with a connection block in databases[]; output:\n%s", out)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("qrdbd start: want a non-zero exit, got %v\noutput: %s", err, out)
		}
		want := `unknown key "databases[0].connection"`
		if !strings.Contains(string(out), want) {
			t.Fatalf("qrdbd start: want %q in the output, got:\n%s", want, out)
		}
		if _, statErr := os.Stat(filepath.Join(dataDir, "databases", "app")); statErr == nil {
			t.Fatalf("qrdbd refused the config but left a store for database app under %s", dataDir)
		}
	})
}
