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

package main

import (
	"context"
	"errors"
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
)

// TestRestoreShardFromBackup_RequiresBackupDir verifies that
// restoreShardFromBackup refuses --restore-backup-id when --backup-dir is
// unset, before touching the host at all (the function's very first check),
// so passing a nil host/createFn here is safe.
func TestRestoreShardFromBackup_RequiresBackupDir(t *testing.T) {
	f := &startFlags{restoreBackupID: "some-backup-id"}

	err := restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error when --backup-dir is unset, got nil")
	}
	var srcErr *RestoreBackupSourceRequiredError
	if !errors.As(err, &srcErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupSourceRequiredError", err, err)
	}
}

// TestRestoreShardFromBackup_MissingBackupFailsVerify verifies that a
// --restore-backup-id naming a backup that does not exist in the
// --backup-dir repository fails during the pre-restore verify step (before
// any shard bootstrap/stop/import), surfacing a wrapped RestoreBackupError
// rather than silently proceeding.
func TestRestoreShardFromBackup_MissingBackupFailsVerify(t *testing.T) {
	f := &startFlags{
		backupDir:       t.TempDir(),
		restoreBackupID: "does-not-exist",
	}

	err := restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: 1})
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error for an unknown backup ID, got nil")
	}
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase != "verify" {
		t.Fatalf("restoreShardFromBackup: Phase = %q, want %q", restoreErr.Phase, "verify")
	}
}
