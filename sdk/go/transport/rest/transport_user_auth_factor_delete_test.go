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
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func isFactorDelete(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/users/factors/delete"
}

func TestRESTDeleteAuthFactor_PostsTheRequest(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var got transport.AuthFactorDeleteRequest
	rs.handle(isFactorDelete, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, transport.AuthFactorDeleteResponse{Status: "deleted"})
	})

	resp, err := rt.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "f1", Force: true})
	if err != nil {
		t.Fatalf("DeleteAuthFactor: %v", err)
	}
	if resp.Status != "deleted" {
		t.Fatalf("Status = %q, want deleted", resp.Status)
	}
	if got.FactorID != "f1" || !got.Force {
		t.Fatalf("server saw %+v, want factor f1 with force", got)
	}
}

func TestRESTDeleteAuthFactor_NonOKIsAnError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(isFactorDelete, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"factor not found"}`))
	})

	if _, err := rt.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "f1"}); err == nil {
		t.Fatal("expected an error for a 404")
	}
}

func TestRESTDeleteAuthFactor_UndecodableBodyIsAnError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(isFactorDelete, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})

	if _, err := rt.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "f1"}); err == nil {
		t.Fatal("expected a decode error")
	}
}
