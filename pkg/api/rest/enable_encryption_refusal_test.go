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

package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// migrationRefusal stands in for a migrator's own typed refusal -- the shape
// sdk/go/node's MigrationNotSupportedError has -- which is NOT the sentinel
// itself but reports as it through errors.Is. A mapping that compared with ==
// instead of errors.Is would miss it.
type migrationRefusal struct{ reason string }

func (e *migrationRefusal) Error() string { return "migration not supported: " + e.reason }

func (e *migrationRefusal) Is(target error) bool {
	return target == service.ErrMigrationProviderUnavailable
}

// TestHandleBarrierEnableEncryption_MigratorRefusalIs501 pins that a WIRED
// migrator declining the migration answers 501 like an unwired one does,
// rather than falling through to 500.
func TestHandleBarrierEnableEncryption_MigratorRefusalIs501(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1), err: &migrationRefusal{reason: "no seal store"}}
	srv := newEnableEncTestServer(t, mig)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d for a migrator refusal", rec.Code, http.StatusNotImplemented)
	}
}

// TestHandleBarrierEnableEncryption_DataPlaneRecyclingIs503 pins that a
// tenant-scope migration arriving while a system-scope one is rebuilding the
// data plane is told to retry (503), not that the node broke (500) or cannot
// migrate (501).
func TestHandleBarrierEnableEncryption_DataPlaneRecyclingIs503(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1), err: &service.DataPlaneRecyclingError{Service: "tenant"}}
	srv := newEnableEncTestServer(t, mig)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"tenant_id":"acme","strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d while the data plane is recycling", rec.Code, http.StatusServiceUnavailable)
	}
	if got := mapHTTPError(&service.DataPlaneRecyclingError{Service: "tenant"}); got != http.StatusServiceUnavailable {
		t.Fatalf("mapHTTPError = %d, want 503", got)
	}
}

// TestHandleBarrierEnableEncryption_NodeClosedIs503 pins that a migration
// refused because the node is closing is retryable (503), not a server fault.
func TestHandleBarrierEnableEncryption_NodeClosedIs503(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1), err: service.ErrNodeClosed}
	srv := newEnableEncTestServer(t, mig)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d for a closing node", rec.Code, http.StatusServiceUnavailable)
	}
	if got := mapHTTPError(service.ErrNodeClosed); got != http.StatusServiceUnavailable {
		t.Fatalf("mapHTTPError = %d, want 503", got)
	}
}
