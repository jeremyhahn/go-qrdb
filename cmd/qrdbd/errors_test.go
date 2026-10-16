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

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

func TestLegacyDataDirError_Message(t *testing.T) {
	e := &LegacyDataDirError{DataDir: "/var/lib/qrdb", Err: seal.ErrLegacyDataDir}
	msg := e.Error()
	if !strings.Contains(msg, "/var/lib/qrdb") {
		t.Errorf("message %q does not include data dir", msg)
	}
	if !strings.Contains(msg, "pre-encfs") {
		t.Errorf("message %q does not mention pre-encfs", msg)
	}
}

func TestLegacyDataDirError_UnwrapMatchesSealSentinel(t *testing.T) {
	e := &LegacyDataDirError{DataDir: "/x", Err: seal.ErrLegacyDataDir}
	if !errors.Is(e, seal.ErrLegacyDataDir) {
		t.Error("errors.Is should match the wrapped seal sentinel")
	}
}

func TestAdminReplicationTimeoutError(t *testing.T) {
	e := &AdminReplicationTimeoutError{Timeout: 30 * time.Second}
	if !strings.Contains(e.Error(), "30s") || !strings.Contains(e.Error(), "not replicated") {
		t.Errorf("message %q missing timeout / not-replicated", e.Error())
	}
	wrapped := &AuthBootstrapError{Step: "wait for admin replication", Err: e}
	var target *AdminReplicationTimeoutError
	if !errors.As(wrapped, &target) {
		t.Errorf("errors.As should extract AdminReplicationTimeoutError, got %v", wrapped)
	}
}

func TestSealedListenerStartupError_Roundtrip(t *testing.T) {
	root := errors.New("listen tcp :8443: bind: address in use")
	e := &SealedListenerStartupError{Err: root}
	if !strings.Contains(e.Error(), "address in use") {
		t.Errorf("message %q lacks underlying detail", e.Error())
	}
	if !errors.Is(e, root) {
		t.Error("errors.Is failed to unwrap")
	}
}

// TestSystemShardServingError carries the readiness wait's typed error: the
// message names the failure, and errors.As reaches the wait's own error.
func TestSystemShardServingError(t *testing.T) {
	inner := &server.ShardReadWaitError{ShardID: 1, Timeout: time.Second, Polls: 3}
	err := error(&SystemShardServingError{Err: inner})
	if !strings.Contains(err.Error(), "system shard not ready to serve") || !strings.Contains(err.Error(), "shard 1") {
		t.Fatalf("message %q", err.Error())
	}
	var got *server.ShardReadWaitError
	if !errors.As(err, &got) || got != inner {
		t.Fatal("SystemShardServingError does not unwrap to the wait's error")
	}
}

// TestStartErrors_MessageNamesWhatToAct carries each start-path error's
// message: it names the value the operator supplied and the remedy, since the
// message is all a failed start leaves on the console.
func TestStartErrors_MessageNamesWhatToAct(t *testing.T) {
	cause := errors.New("permission denied")
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"missing bind host", &MissingBindHostError{}, []string{"no bind host", "server.host", "QRDB_SERVER_HOST", "--host"}},
		{"sm storage mismatch", &SMStorageTypeMismatchError{Storage: "file", SMType: "standard"},
			[]string{`sm-storage "file"`, "--sm disk or --sm fast", `got "standard"`}},
		{"data dir", &DataDirError{Path: "/srv/qrdb", Err: cause}, []string{"data directory /srv/qrdb", "permission denied"}},
		{"restore source required", &RestoreBackupSourceRequiredError{}, []string{"--restore-backup-id requires --backup-dir"}},
		{"restore service type", &RestoreBackupServiceTypeError{}, []string{"internal error", "*service.BackupService"}},
		{"restore phase", &RestoreBackupError{Phase: "verify", BackupID: "b-7", ShardID: 3, Err: cause},
			[]string{`backup "b-7"`, "into shard 3", `during "verify"`, "permission denied"}},
		{"restore verify", &RestoreBackupVerifyError{BackupID: "b-7"}, []string{`backup "b-7"`, "checksum_match=false", "refusing to restore"}},
		{"restore shard absent", &RestoreBackupShardNotInArtifactError{BackupID: "b-7", ShardID: 9}, []string{`backup "b-7"`, "no data for shard 9"}},
		{"system migration seal disabled", &SystemMigrationSealDisabledError{}, []string{"cannot enable encryption", "seal.enabled is false"}},
		{"secure memory mismatch", &SecureMemoryBudgetMismatchError{ConfiguredBytes: 2048, InstalledBytes: 1024},
			[]string{"(1024 bytes)", "configured budget (2048 bytes)", "--secure-memory-budget-mib"}},
		{"inspection opt-out audit", &InspectionOptOutAuditError{Err: cause}, []string{"inspection hardening is disabled", "refused the record", "permission denied"}},
		{"cluster CA persist", &ClusterCAPersistError{Err: cause}, []string{"cluster CA persistence check failed", "permission denied"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q lacks %q", msg, w)
				}
			}
		})
	}
}

// TestStartErrors_UnwrapReachesTheCause: the wrapping start errors keep their
// cause reachable, so a caller can still match the underlying failure.
func TestStartErrors_UnwrapReachesTheCause(t *testing.T) {
	cause := errors.New("disk full")
	for name, err := range map[string]error{
		"data dir":           &DataDirError{Path: "/srv/qrdb", Err: cause},
		"restore phase":      &RestoreBackupError{Phase: "import", Err: cause},
		"inspection opt-out": &InspectionOptOutAuditError{Err: cause},
		"cluster CA persist": &ClusterCAPersistError{Err: cause},
	} {
		if !errors.Is(err, cause) {
			t.Errorf("%s: errors.Is does not reach the cause", name)
		}
	}
	var svcType *RestoreBackupServiceTypeError
	if !errors.As(&RestoreBackupError{Phase: "open-backup-store", Err: &RestoreBackupServiceTypeError{}}, &svcType) {
		t.Error("RestoreBackupError does not unwrap to *RestoreBackupServiceTypeError")
	}
}
