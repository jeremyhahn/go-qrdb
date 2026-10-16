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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// conditionFactorServer wires the production handlers to the real service
// over a store with the guarded writes a conditional add commits through.
func conditionFactorServer(t *testing.T) *Server {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return &Server{userAuthFactorSvc: svc}
}

func postFactor(t *testing.T, s *Server, handler func(http.ResponseWriter, *http.Request), body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/v1/users/factors", bytes.NewReader(raw)))
	return rec
}

func TestREST_AddConditional_refusalCarriesReason(t *testing.T) {
	s := conditionFactorServer(t)
	add := map[string]any{"user_id": 7, "type": "mtls", "credential": []byte("cred-1"), "if_no_active_type": true}

	rec := postFactor(t, s, s.handleUserAuthFactorAdd, add)
	if rec.Code != http.StatusOK {
		t.Fatalf("conditional Add status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var added struct {
		Factor           struct{ ID string } `json:"factor"`
		FactorSetVersion uint64              `json:"factor_set_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || added.FactorSetVersion != 1 {
		t.Fatalf("Add response = %+v, %v; want factor_set_version 1", added, err)
	}

	add["credential"] = []byte("cred-2")
	rec = postFactor(t, s, s.handleUserAuthFactorAdd, add)
	if rec.Code != http.StatusConflict {
		t.Fatalf("refused Add status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var refused errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refused.Code != auth.ReasonFactorConditionFailed {
		t.Fatalf("code = %q, want %q", refused.Code, auth.ReasonFactorConditionFailed)
	}
	got := auth.FactorConditionFailedFromMetadata(refused.Metadata)
	if got.ExistingFactorID != added.Factor.ID || got.UserID != 7 || got.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v, want it to name %s", got, added.Factor.ID)
	}
}

func TestREST_AddConditional_expectedVersionAndList(t *testing.T) {
	s := conditionFactorServer(t)

	rec := postFactor(t, s, s.handleUserAuthFactorAdd, map[string]any{
		"user_id": 7, "type": "mtls", "credential": []byte("cred-1"), "expected_factor_set_version": 0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Add at version 0 status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = postFactor(t, s, s.handleUserAuthFactorAdd, map[string]any{
		"user_id": 7, "type": "fido2", "credential": []byte("cred-2"), "expected_factor_set_version": 0,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale Add status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	listRec := httptest.NewRecorder()
	s.handleUserAuthFactorList(listRec, httptest.NewRequest(http.MethodGet, "/v1/users/factors?user_id=7", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("List status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var list struct {
		Total            int    `json:"total"`
		FactorSetVersion uint64 `json:"factor_set_version"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil || list.Total != 1 || list.FactorSetVersion != 1 {
		t.Fatalf("List = %+v, %v; want 1 factor at version 1", list, err)
	}
}

func TestREST_RotateRefusesAddCondition(t *testing.T) {
	s := conditionFactorServer(t)
	rec := postFactor(t, s, s.handleUserAuthFactorRotate, map[string]any{
		"old_factor_id": "f-1",
		"new_factor":    map[string]any{"type": "mtls", "credential": []byte("cred"), "if_no_active_type": true},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Rotate with a condition status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
