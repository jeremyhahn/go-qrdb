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
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// The vlog handlers decode and encode the vlogwire shapes, which QUIC, MCP
// and the SDK carry too.

// logService returns the configured LogService, or writes 501 and returns
// nil when the node serves no verifiable logs.
func (s *Server) logService(w http.ResponseWriter) service.LogService {
	if s.logSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vlog"})
	}
	return s.logSvc
}

// vlogRef names a log in the caller's tenant and the request's database. The
// tenant is left empty so the service confines the request to the caller's.
func vlogRef(ctx context.Context, log string) service.LogRef {
	return service.LogRef{Database: databaseFromContext(ctx), Log: log}
}

// writeVlogError forwards a request a follower refused to the leader, and
// otherwise writes err with its class's status. body is the request body
// already read, nil for a query.
func (s *Server) writeVlogError(w http.ResponseWriter, r *http.Request, body []byte, err error) {
	if s.tryForwardWrite(w, r, body, err) {
		return
	}
	writeErrorJSON(w, mapHTTPError(err), err)
}

// decodeVlogBody reads the bounded request body into v, returning the raw
// body for forwarding. It reports ok=false once it has written the error.
func (s *Server) decodeVlogBody(w http.ResponseWriter, r *http.Request, v any) (body []byte, ok bool) {
	body, ok = readBoundedBody(w, r, resolveMaxBodyBytes(s.maxBodyBytes))
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return nil, false
	}
	return body, true
}

// vlogQueryLog returns the required log query parameter, writing 400 when it
// is missing.
func vlogQueryLog(w http.ResponseWriter, r *http.Request) (string, bool) {
	log := r.URL.Query().Get("log")
	if log == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "log"})
		return "", false
	}
	return log, true
}

// vlogQueryUint parses the named query parameter, writing 400 when it is
// malformed, or missing and required. An absent optional one is zero.
func vlogQueryUint(w http.ResponseWriter, r *http.Request, param string, required bool) (uint64, bool) {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		if required {
			writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: param})
			return 0, false
		}
		return 0, true
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: param, Err: err})
		return 0, false
	}
	return v, true
}

// handleVlogCreate creates a verifiable log.
func (s *Server) handleVlogCreate(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	var req vlogwire.CreateRequest
	body, ok := s.decodeVlogBody(w, r, &req)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.CreateLog(ctx, &service.CreateLogRequest{
		LogRef:            vlogRef(ctx, req.Log),
		Origin:            req.Origin,
		VerifierKeys:      req.VerifierKeys,
		AllowExpireBodies: req.AllowExpireBodies,
		SigningKeyType:    req.SigningKeyType,
	})
	if err != nil {
		s.writeVlogError(w, r, body, err)
		return
	}
	writeJSON(w, vlogwire.Create(resp.ID, resp.Key))
}

// handleVlogList lists the verifiable logs in the caller's tenant and
// database.
func (s *Server) handleVlogList(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.ListLogs(ctx, &service.ListLogsRequest{Database: databaseFromContext(ctx)})
	if err != nil {
		s.writeVlogError(w, r, nil, err)
		return
	}
	writeJSON(w, vlogwire.List(resp.Logs))
}

// handleVlogAppend appends entries to a verifiable log.
func (s *Server) handleVlogAppend(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	var req vlogwire.AppendRequest
	body, ok := s.decodeVlogBody(w, r, &req)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.Append(ctx, &service.AppendRequest{LogRef: vlogRef(ctx, req.Log), Entries: req.Entries})
	if err != nil {
		s.writeVlogError(w, r, body, err)
		return
	}
	writeJSON(w, &vlogwire.AppendResult{FirstIndex: resp.FirstIndex, Count: resp.Count})
}

// handleVlogGet reads a range of a verifiable log's entries.
func (s *Server) handleVlogGet(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	log, ok := vlogQueryLog(w, r)
	if !ok {
		return
	}
	from, ok := vlogQueryUint(w, r, "from", false)
	if !ok {
		return
	}
	limit, ok := vlogQueryUint(w, r, "limit", false)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.GetEntries(ctx, &service.GetEntriesRequest{LogRef: vlogRef(ctx, log), From: from, Limit: int(limit)})
	if err != nil {
		s.writeVlogError(w, r, nil, err)
		return
	}
	writeJSON(w, vlogwire.Get(resp.Entries))
}

// handleVlogProveInclusion returns an inclusion proof for one leaf.
func (s *Server) handleVlogProveInclusion(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	log, ok := vlogQueryLog(w, r)
	if !ok {
		return
	}
	index, ok := vlogQueryUint(w, r, "index", true)
	if !ok {
		return
	}
	size, ok := vlogQueryUint(w, r, "size", true)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.ProveInclusion(ctx, &service.InclusionRequest{LogRef: vlogRef(ctx, log), Index: index, Size: size})
	if err != nil {
		s.writeVlogError(w, r, nil, err)
		return
	}
	writeJSON(w, &vlogwire.InclusionProof{Index: resp.Index, Size: resp.Size, Hashes: resp.Hashes})
}

// handleVlogProveConsistency returns a consistency proof between two tree
// sizes.
func (s *Server) handleVlogProveConsistency(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	log, ok := vlogQueryLog(w, r)
	if !ok {
		return
	}
	size1, ok := vlogQueryUint(w, r, "size1", true)
	if !ok {
		return
	}
	size2, ok := vlogQueryUint(w, r, "size2", true)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.ProveConsistency(ctx, &service.ConsistencyRequest{LogRef: vlogRef(ctx, log), Size1: size1, Size2: size2})
	if err != nil {
		s.writeVlogError(w, r, nil, err)
		return
	}
	writeJSON(w, &vlogwire.ConsistencyProof{Size1: resp.Size1, Size2: resp.Size2, Hashes: resp.Hashes})
}

// handleVlogHead returns a verifiable log's head.
func (s *Server) handleVlogHead(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	log, ok := vlogQueryLog(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.Head(ctx, &service.HeadRequest{LogRef: vlogRef(ctx, log)})
	if err != nil {
		s.writeVlogError(w, r, nil, err)
		return
	}
	writeJSON(w, vlogwire.HeadOf(resp.Head))
}

// handleVlogSignHead signs a verifiable log's current head.
func (s *Server) handleVlogSignHead(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	var req vlogwire.LogRequest
	body, ok := s.decodeVlogBody(w, r, &req)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.SignHead(ctx, &service.SignHeadRequest{LogRef: vlogRef(ctx, req.Log)})
	if err != nil {
		s.writeVlogError(w, r, body, err)
		return
	}
	writeJSON(w, &vlogwire.SignHeadResult{Head: resp.Head, Checkpoint: resp.Checkpoint})
}

// handleVlogRotateKey appends a new head key of the named type to a log's
// verifier-key list; heads signed from then on name it, and a checkpoint
// signed under an earlier key still verifies under that key.
func (s *Server) handleVlogRotateKey(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	var req vlogwire.RotateKeyRequest
	body, ok := s.decodeVlogBody(w, r, &req)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.RotateKey(ctx, &service.RotateKeyRequest{LogRef: vlogRef(ctx, req.Log), SigningKeyType: req.SigningKeyType})
	if err != nil {
		s.writeVlogError(w, r, body, err)
		return
	}
	writeJSON(w, &vlogwire.RotateKeyResult{Key: vlogwire.Key(resp.Key)})
}

// handleVlogExpireBodies drops the entry bodies below a cutoff.
func (s *Server) handleVlogExpireBodies(w http.ResponseWriter, r *http.Request) {
	logSvc := s.logService(w)
	if logSvc == nil {
		return
	}
	var req vlogwire.ExpireRequest
	body, ok := s.decodeVlogBody(w, r, &req)
	if !ok {
		return
	}
	ctx := r.Context()
	resp, err := logSvc.ExpireBodies(ctx, &service.ExpireBodiesRequest{LogRef: vlogRef(ctx, req.Log), Cutoff: req.Cutoff})
	if err != nil {
		s.writeVlogError(w, r, body, err)
		return
	}
	writeJSON(w, &vlogwire.ExpireResult{Expired: resp.Expired})
}
