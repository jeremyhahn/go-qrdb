// Copyright (c) 2026 Jeremy Hahn
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

package rest

import (
	"context"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The /v1/seal-store routes serve the node's seal store, the generic secret
// store a seal strategy keeps for its own state. Every route is gated on
// seal:store_manage, a node-scoped permission: a tenant-scoped identity is
// refused before the handler runs. A node with no seal store wired does not
// register the handlers (routeIf).

// sealStorePutRequest is the POST /v1/seal-store/secrets/{name} body. The
// secret travels base64-encoded ([]byte in JSON).
type sealStorePutRequest struct {
	Secret []byte `json:"secret"`
}

// sealStoreSecretResponse is the GET /v1/seal-store/secrets/{name} body.
type sealStoreSecretResponse struct {
	Name   string `json:"name"`
	Secret []byte `json:"secret"`
}

// sealStoreListResponse is the GET /v1/seal-store/secrets body.
type sealStoreListResponse struct {
	Names []string `json:"names"`
}

// handleSealStorePut handles POST /v1/seal-store/secrets/{name}.
func (s *Server) handleSealStorePut(w http.ResponseWriter, r *http.Request) {
	var req sealStorePutRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := s.sealStoreSvc.Put(ctx, r.PathValue("name"), req.Secret); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSealStoreGet handles GET /v1/seal-store/secrets/{name}.
func (s *Server) handleSealStoreGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	name := r.PathValue("name")
	secret, err := s.sealStoreSvc.Get(ctx, name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, sealStoreSecretResponse{Name: name, Secret: secret})
}

// handleSealStoreDelete handles DELETE /v1/seal-store/secrets/{name}.
func (s *Server) handleSealStoreDelete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := s.sealStoreSvc.Delete(ctx, r.PathValue("name")); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSealStoreList handles GET /v1/seal-store/secrets.
func (s *Server) handleSealStoreList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	names, err := s.sealStoreSvc.List(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, sealStoreListResponse{Names: names})
}

// handleSealStoreReseal handles POST /v1/seal-store/secrets/{name}/reseal.
func (s *Server) handleSealStoreReseal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := s.sealStoreSvc.Reseal(ctx, r.PathValue("name")); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSealStoreStatus handles GET /v1/seal-store/status.
func (s *Server) handleSealStoreStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	st, err := s.sealStoreSvc.Status(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, transport.SealStoreStatusResponse{
		Available:   st.Available,
		SealerID:    string(st.Strategy),
		SecretCount: st.SecretCount,
		SecretNames: st.SecretNames,
	})
}
