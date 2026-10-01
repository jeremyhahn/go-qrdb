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
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Credential request types (REST-local, unexported)
// ---------------------------------------------------------------------------

// credentialSubmitRequest is the JSON payload for POST /v1/credentials/submit.
// Value is a []byte so encoding/json decodes it from base64 (RFC 4648),
// preserving arbitrary binary credential material byte-for-byte. Malformed
// base64 surfaces as a JSON decode error (400) via the standard decoder.
type credentialSubmitRequest struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// ---------------------------------------------------------------------------
// Credential URL prefix
// ---------------------------------------------------------------------------

// credentialsPathPrefix is the URL prefix every credential route shares.
const credentialsPathPrefix = "/v1/credentials/"

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

	if err := s.credentialSvc.CredentialSubmit(r.Context(), req.Name, req.Value); err != nil {
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

	writeJSON(w, transport.CredentialStrategyResponse{
		Strategy: s.credentialSvc.CredentialStrategy(),
	})
}

// handleCredentialDelete handles DELETE /v1/credentials/{name} -- removes the
// credential with the given name extracted from the URL path.
func (s *Server) handleCredentialDelete(w http.ResponseWriter, r *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	name := strings.TrimPrefix(r.URL.Path, credentialsPathPrefix)
	if name == "" || name == r.URL.Path {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	if err := s.credentialSvc.CredentialDelete(r.Context(), name); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCredentialList handles GET /v1/credentials -- returns the sorted
// list of every stored credential name. Values are never returned.
func (s *Server) handleCredentialList(w http.ResponseWriter, r *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	names, err := s.credentialSvc.CredentialList(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	if names == nil {
		names = []string{}
	}

	writeJSON(w, transport.CredentialListResponse{Names: names})
}

// handleCredentialRotate handles POST /v1/credentials/{name}/rotate --
// atomically replaces the current value of the named credential, optionally
// keeping the previous version retrievable for a grace period.
func (s *Server) handleCredentialRotate(w http.ResponseWriter, r *http.Request, name string) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	var req transport.CredentialRotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	// The path-param name authoritatively identifies the credential;
	// override any conflicting name in the body so a single source of
	// truth controls which credential is rotated.
	req.Name = name

	opts := credentials.RotateOptions{
		NewValue:    req.NewValue,
		GracePeriod: time.Duration(req.GracePeriodSeconds) * time.Second,
	}

	result, err := s.credentialSvc.CredentialRotate(r.Context(), name, opts)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	resp := transport.CredentialRotateResponse{
		Name:            result.Name,
		PreviousVersion: result.PreviousVersion,
		NewVersion:      result.NewVersion,
	}
	if !result.GraceUntil.IsZero() {
		resp.GraceUntilUnix = result.GraceUntil.Unix()
	}
	writeJSON(w, resp)
}

// handleCredentialRetrieveVersion handles
// GET /v1/credentials/{name}/versions/{version} -- returns the credential
// value for the requested version. Version "0" means the current version.
func (s *Server) handleCredentialRetrieveVersion(w http.ResponseWriter, r *http.Request, name, versionStr string) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}
	version, err := strconv.ParseUint(versionStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "version", Err: err})
		return
	}

	value, err := s.credentialSvc.CredentialRetrieveVersion(r.Context(), name, version)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, transport.CredentialRetrieveVersionResponse{Value: value})
}

// handleCredentialListVersions handles GET /v1/credentials/{name}/versions
// -- returns the version history (current + non-expired grace) for the
// named credential. Values are never returned.
func (s *Server) handleCredentialListVersions(w http.ResponseWriter, r *http.Request, name string) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	versions, err := s.credentialSvc.CredentialListVersions(r.Context(), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	out := transport.CredentialListVersionsResponse{
		Name:     name,
		Versions: make([]transport.CredentialVersionInfo, 0, len(versions)),
	}
	for _, v := range versions {
		entry := transport.CredentialVersionInfo{
			Version:   v.Version,
			IsCurrent: v.IsCurrent,
		}
		if !v.CreatedAt.IsZero() {
			entry.CreatedAtUnix = v.CreatedAt.Unix()
		}
		if !v.GraceUntil.IsZero() {
			entry.GraceUntilUnix = v.GraceUntil.Unix()
		}
		out.Versions = append(out.Versions, entry)
	}
	writeJSON(w, out)
}

// handleCredentialGCExpired handles POST /v1/credentials/{name}/gc --
// reaps any historical versions whose grace period has elapsed for the
// named credential. Returns the count reaped.
func (s *Server) handleCredentialGCExpired(w http.ResponseWriter, r *http.Request, name string) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	reaped, err := s.credentialSvc.CredentialGCExpired(r.Context(), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, transport.CredentialGCResponse{Reaped: reaped})
}

// ---------------------------------------------------------------------------
// Credential path dispatch
// ---------------------------------------------------------------------------

// handleCredentialByPath routes requests under /v1/credentials/{name}/...
// to the appropriate versioned-rotation handler. The dispatcher splits
// on '/' rather than substring-matching so a credential whose name
// happens to contain "rotate" or "versions" cannot be confused with a
// sub-route.
//
// Routes handled:
//   - DELETE /v1/credentials/{name}                       -> delete
//   - POST   /v1/credentials/{name}/rotate                -> rotate
//   - POST   /v1/credentials/{name}/gc                    -> gc expired
//   - GET    /v1/credentials/{name}/versions              -> list versions
//   - GET    /v1/credentials/{name}/versions/{version}    -> retrieve version
func (s *Server) handleCredentialByPath(w http.ResponseWriter, r *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, credentialsPathPrefix)
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	parts := strings.Split(rest, "/")
	name := parts[0]
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	switch len(parts) {
	case 1:
		// /v1/credentials/{name} — currently only DELETE.
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", "DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleCredentialDelete(w, r)
	case 2:
		// /v1/credentials/{name}/{op}
		switch parts[1] {
		case "rotate":
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleCredentialRotate(w, r, name)
		case "gc":
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleCredentialGCExpired(w, r, name)
		case "versions":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleCredentialListVersions(w, r, name)
		case "value":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleCredentialRetrieve(w, r, name)
		default:
			http.NotFound(w, r)
		}
	case 3:
		// /v1/credentials/{name}/versions/{version}
		if parts[1] != "versions" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleCredentialRetrieveVersion(w, r, name, parts[2])
	default:
		http.NotFound(w, r)
	}
}

// ---------------------------------------------------------------------------
// Credential error mapping
// ---------------------------------------------------------------------------

// credentialStoreRequest is the JSON payload for POST /v1/credentials/store.
// Value is a []byte so encoding/json decodes it from base64 (RFC 4648),
// preserving arbitrary binary credential material byte-for-byte.
type credentialStoreRequest struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// credentialValueResponse is the JSON payload returned by
// GET /v1/credentials/{name}/value. Value is a []byte so encoding/json
// renders it as base64 (RFC 4648), preserving binary content byte-for-byte.
type credentialValueResponse struct {
	Value []byte `json:"value"`
}

// handleCredentialStore handles POST /v1/credentials/store -- persists a
// credential value under the given name.
func (s *Server) handleCredentialStore(w http.ResponseWriter, r *http.Request) {
	if s.credentialSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "credentials"})
		return
	}

	var req credentialStoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.credentialSvc.CredentialStore(r.Context(), req.Name, req.Value); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCredentialRetrieve handles GET /v1/credentials/{name}/value -- returns
// the credential value for the given name.
func (s *Server) handleCredentialRetrieve(w http.ResponseWriter, r *http.Request, name string) {
	value, err := s.credentialSvc.CredentialRetrieve(r.Context(), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, credentialValueResponse{Value: value})
}
