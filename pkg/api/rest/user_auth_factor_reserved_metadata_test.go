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

// REST half of the go-qrdb#234 cross-transport verification.
//
// These drive the PRODUCTION handlers against the REAL user auth-factor
// service. A mock service would prove nothing here: the rule lives in the
// service, so a test that stubs the service asserts only that the stub
// returned what it was told to.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// realFactorServer wires the production REST server to a real user auth-factor
// service over an in-memory KV, so the request travels the whole path the
// operator's request does.
func realFactorServer(t *testing.T) *Server {
	t.Helper()
	store := map[string][]byte{}
	kv := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			store[key] = append([]byte(nil), value...)
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			// The service reads a miss as "not found" from either a nil
			// error with an empty payload or an error, so an empty read
			// is the honest representation of an absent key here.
			return store[key], nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var out []string
			for k := range store {
				if strings.HasPrefix(k, prefix) {
					out = append(out, k)
				}
			}
			return out, nil
		},
	}
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kv})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return &Server{userAuthFactorSvc: svc}
}

// TestREST_AddRefusesReservedMetadata: the production POST handler must answer
// 400 and name both the key and the reserved prefix. Before #234 it copied the
// key onto the stored factor and answered 200.
func TestREST_AddRefusesReservedMetadata(t *testing.T) {
	s := realFactorServer(t)

	body, _ := json.Marshal(map[string]any{
		"user_id":    7,
		"type":       "mtls",
		"credential": []byte("cred"),
		"metadata":   map[string]string{"attest_level": "hardware"},
	})
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, httptest.NewRequest(http.MethodPost, "/v1/users/factors", bytes.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "attest_level") {
		t.Errorf("response does not name the offending key: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), auth.ReservedMetadataPrefix) {
		t.Errorf("response does not name the reserved prefix: %s", rec.Body.String())
	}
}

// TestREST_RotateRefusesReservedMetadata covers the other write path.
func TestREST_RotateRefusesReservedMetadata(t *testing.T) {
	s := realFactorServer(t)

	// Seed a factor to rotate away from.
	seed, _ := json.Marshal(map[string]any{
		"user_id": 7, "type": "mtls", "credential": []byte("cred-old"),
	})
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, httptest.NewRequest(http.MethodPost, "/v1/users/factors", bytes.NewReader(seed)))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed Add status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var added struct {
		Factor struct {
			ID string `json:"id"`
		} `json:"factor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode seed response: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"old_factor_id": added.Factor.ID,
		"new_factor": map[string]any{
			"user_id":    7,
			"type":       "mtls",
			"credential": []byte("cred-new"),
			"metadata":   map[string]string{"attest_aaguid": "forged"},
		},
	})
	rec2 := httptest.NewRecorder()
	s.handleUserAuthFactorRotate(rec2, httptest.NewRequest(http.MethodPost, "/v1/users/factors/rotate", bytes.NewReader(body)))

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("rotate status = %d, want %d; body=%s", rec2.Code, http.StatusBadRequest, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "attest_aaguid") {
		t.Errorf("response does not name the offending key: %s", rec2.Body.String())
	}
}

// TestREST_AddAllowsOrdinaryMetadata is the control: the rule is a namespace,
// not a ban on metadata, and the REST path must still carry ordinary keys.
func TestREST_AddAllowsOrdinaryMetadata(t *testing.T) {
	s := realFactorServer(t)

	body, _ := json.Marshal(map[string]any{
		"user_id":    7,
		"type":       "mtls",
		"credential": []byte("cred"),
		"metadata":   map[string]string{"vendor": "yubico"},
	})
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, httptest.NewRequest(http.MethodPost, "/v1/users/factors", bytes.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "yubico") {
		t.Errorf("ordinary metadata not round-tripped: %s", rec.Body.String())
	}
}

// TestMapUserAuthFactorError_ReservedMetadata pins the status directly, so a
// later edit to the switch cannot quietly send this to the 500 arm it would
// have fallen through to before #234.
func TestMapUserAuthFactorError_ReservedMetadata(t *testing.T) {
	got := mapHTTPError(&auth.ReservedMetadataKeyError{
		Key: "attest_level", Prefix: auth.ReservedMetadataPrefix,
	})
	if got != http.StatusBadRequest {
		t.Fatalf("mapHTTPError = %d, want %d", got, http.StatusBadRequest)
	}
}
