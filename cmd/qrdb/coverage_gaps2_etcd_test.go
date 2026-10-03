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

//go:build qrdb_embedded && qrdb_etcd && !integration

package main

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// update-etcd
//
// This subcommand is tagged qrdb_etcd (database_etcd_cmd.go): on a binary
// without it, "update-etcd" does not exist as a subcommand at all, so these
// tests -- which expect the command to succeed, not merely to fail somehow
// -- carry the same constraint rather than the untagged coverage_gaps2_test.go
// they used to live in. A binary without qrdb_etcd has no way to exercise
// this behavior, so there is nothing to test there, not a gap to cover.
// ---------------------------------------------------------------------------

func TestDatabaseUpdateEtcd_EnabledWithListenAddr(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateEtcdFn = func(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  "kv",
			Backend: "pebble",
			Status:  "open",
			Protocol: database.ProtocolConfig{
				Etcd: &database.EtcdConfig{Enabled: true, ListenAddr: cfg.ListenAddr},
			},
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-etcd", "mydb",
		"--enabled",
		"--listen-addr", "localhost:2379",
	)
	if err != nil {
		t.Fatalf("update-etcd --enabled error: %v", err)
	}
	if !strings.Contains(output, "Etcd:") {
		t.Errorf("output missing Etcd:, got: %q", output)
	}
}

func TestDatabaseUpdateEtcd_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := newFullFnDatabaseService()
	svc.updateEtcdFn = func(name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{Name: name, Engine: "kv", Backend: "pebble", Status: "open"}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-etcd", "mydb", "--enabled")
	if err != nil {
		t.Fatalf("update-etcd json error: %v", err)
	}
	if !strings.Contains(output, `"name"`) {
		t.Errorf("JSON output missing name, got: %q", output)
	}
}

func TestDatabaseUpdateEtcd_ServiceError(t *testing.T) {
	svc := newFullFnDatabaseService()
	svc.updateEtcdFn = func(name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}

	_, err := executeDatabaseCmd(t, svc, "update-etcd", "nonexistent")
	if err == nil {
		t.Fatal("expected error from update-etcd when database not found")
	}
}
