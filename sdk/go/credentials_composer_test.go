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

package sdk_test

import (
	"errors"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// TestCredentialComposer_SDKOnlyImport pins Codex round 1 on #1548: an SDK
// composer builds a rotation policy, reads a policy status and handles a
// migration conflict with only the sdk/go import.
func TestCredentialComposer_SDKOnlyImport(t *testing.T) {
	policy := sdk.RotationPolicy{Enabled: true, Interval: 24 * time.Hour, RetainVersions: 3}
	status := sdk.CredentialRotationPolicyStatus{Policy: policy}
	if status.Policy != policy {
		t.Fatalf("status policy = %+v, want %+v", status.Policy, policy)
	}

	var err error = &sdk.CredentialMigrationConflictError{Name: "db-pass", Value: true}
	var conflict *sdk.CredentialMigrationConflictError
	if !errors.As(err, &conflict) || conflict.Name != "db-pass" || !conflict.Value {
		t.Fatalf("errors.As(%v) = %+v", err, conflict)
	}

	for _, how := range []sdk.MigrationResolution{sdk.MigrationImport, sdk.MigrationDiscard} {
		if how == "" {
			t.Fatal("empty migration resolution constant")
		}
	}
	if sdk.MigrationImport == sdk.MigrationDiscard {
		t.Fatal("migration resolutions are not distinct")
	}
}
