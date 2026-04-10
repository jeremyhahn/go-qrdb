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
	"strings"
)

// ---------------------------------------------------------------------------
// Credential request types (REST-local, unexported)
// ---------------------------------------------------------------------------

// credentialSubmitRequest is the JSON payload for POST /v1/credentials/submit.
type credentialSubmitRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"` // base64 or plain text
}

// ---------------------------------------------------------------------------
// Credential handlers
// ---------------------------------------------------------------------------

// handleCredentialSubmit handles POST /v1/credentials/submit -- submits a
// credential value for the named credential, notifying any blocked retrievers
// for the manual strategy or delegating to storage for the barrier strategy.
func (s *Server) handleCredentialSubmit(w http.ResponseWriter, r *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	var req credentialSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.credentialSvc.CredentialSubmit(r.Context(), req.Name, []byte(req.Value)); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCredentialStrategy handles GET /v1/credentials/strategy -- returns
// the active credential strategy name.
func (s *Server) handleCredentialStrategy(w http.ResponseWriter, _ *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	writeJSON(w, map[string]interface{}{
		"strategy": s.credentialSvc.CredentialStrategy(),
	})
}

// handleCredentialDelete handles DELETE /v1/credentials/{name} -- removes the
// credential with the given name extracted from the URL path.
func (s *Server) handleCredentialDelete(w http.ResponseWriter, r *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/v1/credentials/")
	if name == "" || name == r.URL.Path {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "name"})
		return
	}

	if err := s.credentialSvc.CredentialDelete(r.Context(), name); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
