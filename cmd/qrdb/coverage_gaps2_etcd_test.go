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
