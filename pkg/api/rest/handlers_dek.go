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
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dekwire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Tenant DEK keyring REST handlers
//
// Routes:
//   GET  /v1/dek/status[?tenant_id=]  -> Status
//   POST /v1/dek/rotate               -> Rotate
//   POST /v1/dek/retire               -> Retire
//   POST /v1/dek/encrypt              -> Encrypt
//   POST /v1/dek/decrypt              -> Decrypt
//   POST /v1/dek/rewrap               -> Rewrap
//
// Every route acts on the caller's tenant; service.DEKServicer refuses any
// other with 403. Byte fields travel base64-encoded in JSON.
// ---------------------------------------------------------------------------

// handleDEKStatus handles GET /v1/dek/status.
func (s *Server) handleDEKStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.dekSvc.Status(r.Context(), r.URL.Query().Get("tenant_id"))
	if err != nil {
		writeDEKError(w, err)
		return
	}
	writeJSON(w, dekwire.Status(st))
}

// handleDEKRotate handles POST /v1/dek/rotate.
func (s *Server) handleDEKRotate(w http.ResponseWriter, r *http.Request) {
	var req transport.TenantDEKRequest
	if !decodeDEKBody(w, r, &req) {
		return
	}
	st, err := s.dekSvc.Rotate(r.Context(), req.TenantID)
	if err != nil {
		writeDEKError(w, err)
		return
	}
	writeJSON(w, dekwire.Status(st))
}

// handleDEKRetire handles POST /v1/dek/retire.
func (s *Server) handleDEKRetire(w http.ResponseWriter, r *http.Request) {
	var req transport.TenantDEKRetireRequest
	if !decodeDEKBody(w, r, &req) {
		return
	}
	st, err := s.dekSvc.Retire(r.Context(), req.TenantID, req.Version)
	if err != nil {
		writeDEKError(w, err)
		return
	}
	writeJSON(w, dekwire.Status(st))
}

// handleDEKEncrypt handles POST /v1/dek/encrypt.
func (s *Server) handleDEKEncrypt(w http.ResponseWriter, r *http.Request) {
	var req transport.TenantDEKEncryptRequest
	if !decodeDEKBody(w, r, &req) {
		return
	}
	sealed, err := s.dekSvc.Encrypt(r.Context(), req.TenantID, req.Plaintext, req.AAD)
	if err != nil {
		writeDEKError(w, err)
		return
	}
	writeJSON(w, dekwire.Encrypt(sealed))
}

// handleDEKDecrypt handles POST /v1/dek/decrypt.
func (s *Server) handleDEKDecrypt(w http.ResponseWriter, r *http.Request) {
	var req transport.TenantDEKDecryptRequest
	if !decodeDEKBody(w, r, &req) {
		return
	}
	opened, err := s.dekSvc.Decrypt(r.Context(), req.TenantID, req.Ciphertext, req.AAD)
	if err != nil {
		writeDEKError(w, err)
		return
	}
	writeJSON(w, dekwire.Decrypt(opened))
}

// handleDEKRewrap handles POST /v1/dek/rewrap.
func (s *Server) handleDEKRewrap(w http.ResponseWriter, r *http.Request) {
	var req transport.TenantDEKRewrapRequest
	if !decodeDEKBody(w, r, &req) {
		return
	}
	rw, err := s.dekSvc.Rewrap(r.Context(), req.TenantID, req.Ciphertext, req.AAD)
	if err != nil {
		writeDEKError(w, err)
		return
	}
	writeJSON(w, dekwire.Rewrap(rw))
}

// decodeDEKBody decodes a JSON request body into req, answering 400 and
// returning false when it cannot.
func decodeDEKBody(w http.ResponseWriter, r *http.Request, req any) bool {
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return false
	}
	return true
}

// writeDEKError answers a keyring error with the status its class renders
// to on this transport.
func writeDEKError(w http.ResponseWriter, err error) {
	writeErrorJSON(w, mapHTTPError(err), err)
}
