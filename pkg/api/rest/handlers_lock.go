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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// ---------------------------------------------------------------------------
// Lock request/response types
// ---------------------------------------------------------------------------

// lockAcquireRequest is the JSON payload for POST /lock/acquire and
// POST /lock/trylock.
type lockAcquireRequest struct {
	Key       string `json:"key"`
	HolderID  string `json:"holder_id"`
	TTL       int64  `json:"ttl"`
	Reentrant bool   `json:"reentrant"`
}

// lockReleaseRequest is the JSON payload for POST /lock/release.
type lockReleaseRequest struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
}

// lockExtendRequest is the JSON payload for POST /lock/extend.
type lockExtendRequest struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
}

// lockResponse is the JSON representation of an acquired distributed lock.
type lockResponse struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
	LeaseID      int64  `json:"lease_id"`
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"`
	TTL          int64  `json:"ttl"`
	AcquiredAt   int64  `json:"acquired_at"`
}

// locksResponse is the JSON payload returned by GET /lock/list.
type locksResponse struct {
	Locks []*lockResponse `json:"locks"`
}

// ---------------------------------------------------------------------------
// Lock handlers
// ---------------------------------------------------------------------------

// handleLockAcquire handles POST /lock/acquire -- acquires a distributed
// lock, blocking until acquired or the context is cancelled.
func (s *Server) handleLockAcquire(w http.ResponseWriter, r *http.Request) {
	if s.lockSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
		return
	}

	var req lockAcquireRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	l, err := s.lockSvc.Lock(r.Context(), req.Key, req.HolderID, time.Duration(req.TTL)*time.Second, req.Reentrant)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, lockToResponse(l))
}

// handleLockTryLock handles POST /lock/trylock -- attempts to acquire a
// lock immediately without blocking.
func (s *Server) handleLockTryLock(w http.ResponseWriter, r *http.Request) {
	if s.lockSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
		return
	}

	var req lockAcquireRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	l, err := s.lockSvc.TryLock(r.Context(), req.Key, req.HolderID, time.Duration(req.TTL)*time.Second, req.Reentrant)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, lockToResponse(l))
}

// handleLockRelease handles POST /lock/release -- releases a held lock.
// The fencing token must match the current lock state.
func (s *Server) handleLockRelease(w http.ResponseWriter, r *http.Request) {
	if s.lockSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
		return
	}

	var req lockReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.lockSvc.Unlock(r.Context(), req.Key, req.FencingToken); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, struct {
		Status string `json:"status"`
	}{Status: "released"})
}

// handleLockExtend handles POST /lock/extend -- refreshes the TTL on a
// held lock without releasing it. The fencing token must match.
func (s *Server) handleLockExtend(w http.ResponseWriter, r *http.Request) {
	if s.lockSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
		return
	}

	var req lockExtendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	l, err := s.lockSvc.Extend(r.Context(), req.Key, req.FencingToken)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, lockToResponse(l))
}

// handleLockStatus handles GET /lock/status?key=... -- returns the
// current lock state for a key, or 404 if the key is not locked.
func (s *Server) handleLockStatus(w http.ResponseWriter, r *http.Request) {
	if s.lockSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "key"})
		return
	}

	l, err := s.lockSvc.Status(r.Context(), key)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	if l == nil {
		notFound := &lock.LockNotFoundError{Key: key}
		writeErrorJSON(w, http.StatusNotFound, notFound)
		return
	}

	writeJSON(w, lockToResponse(l))
}

// handleLockList handles GET /lock/list?prefix=... -- returns all active
// locks, optionally filtered by key prefix.
func (s *Server) handleLockList(w http.ResponseWriter, r *http.Request) {
	if s.lockSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lock"})
		return
	}

	prefix := r.URL.Query().Get("prefix")

	locks, err := s.lockSvc.List(r.Context(), prefix)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	resp := make([]*lockResponse, len(locks))
	for i, l := range locks {
		resp[i] = lockToResponse(l)
	}

	writeJSON(w, locksResponse{Locks: resp})
}

// ---------------------------------------------------------------------------
// Lock conversion helpers
// ---------------------------------------------------------------------------

// lockToResponse converts a lock.Lock to its JSON response representation.
func lockToResponse(l *lock.Lock) *lockResponse {
	if l == nil {
		return nil
	}
	return &lockResponse{
		Key:          l.Key,
		FencingToken: l.FencingToken,
		LeaseID:      l.LeaseID,
		HolderID:     l.HolderID,
		Reentrant:    l.Reentrant,
		AcquireCount: l.AcquireCount,
		TTL:          l.TTL,
		AcquiredAt:   l.AcquiredAt,
	}
}
