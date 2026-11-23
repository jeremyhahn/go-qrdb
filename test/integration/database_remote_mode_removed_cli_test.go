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
	"fmt"
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

// TestDatabase_CLI_RemoteModeRefused_PerProtocol asserts, on every
// transport, that `database create` takes no remote-mode or connection flag:
// each is refused as an unknown flag, no database is created, and the same
// create without it succeeds as a local database (OD-9 A).
func TestDatabase_CLI_RemoteModeRefused_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	refused := [][]string{
		{"--mode", "remote"},
		{"--remote"},
		{"--address", "127.0.0.1:9443"},
		{"--conn-transport", "grpc"},
		{"--connection", "{}"},
	}
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			dbName := fmt.Sprintf("noremotecreate-%s-%d", proto.name, time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
			})
			for _, flags := range refused {
				args := append([]string{"database", "create", dbName, "--backend", "pebble"}, flags...)
				out, err := runCLIProto(t, node, proto, args...)
				if err == nil {
					t.Fatalf("[%s] database create %v: want unknown flag, got success\noutput: %s", proto.name, flags, out)
				}
				if !strings.Contains(out+err.Error(), "unknown flag") {
					t.Fatalf("[%s] database create %v: want unknown flag, got %v\noutput: %s", proto.name, flags, err, out)
				}
				if info, err := runCLIProto(t, node, proto, "database", "info", dbName); err == nil {
					t.Fatalf("[%s] database create %v was refused but created %s:\n%s", proto.name, flags, dbName, info)
				}
			}

			if out, err := runCLIProto(t, node, proto, "database", "create", dbName, "--backend", "pebble"); err != nil {
				t.Fatalf("[%s] database create %s: %v\noutput: %s", proto.name, dbName, err, out)
			}
			raw, err := runCLIProto(t, node, proto, "--output", "json", "database", "info", dbName)
			if err != nil {
				t.Fatalf("[%s] database info %s: %v\noutput: %s", proto.name, dbName, err, raw)
			}
			lower := strings.ToLower(raw)
			if strings.Contains(lower, `"connection"`) || strings.Contains(lower, `"remote"`) || !strings.Contains(lower, `"mode": "embedded"`) {
				t.Fatalf("[%s] database info %s names remote mode or a connection:\n%s", proto.name, dbName, raw)
			}
		})
	}
}
