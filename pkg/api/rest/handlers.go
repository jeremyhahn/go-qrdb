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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// statusResponse is the JSON payload returned by GET /status. The Shards
// field uses any to avoid importing quicraft types into the REST layer;
// the service layer provides quicraft.ShardInfo values that serialize
// correctly via their JSON struct tags.
type statusResponse struct {
	NodeID      string `json:"node_id"`
	RaftAddress string `json:"raft_address"`
	IsLeader    bool   `json:"is_leader"`
	LeaderID    uint64 `json:"leader_id"`
	ShardCount  int    `json:"shard_count"`
	IsDraining  bool   `json:"is_draining"`
	Shards      any    `json:"shards"`
}

// peersResponse is the JSON payload returned by GET /peers.
type peersResponse struct {
	Shards []shardPeers `json:"shards"`
}

// shardPeers describes the peers for a single shard.
type shardPeers struct {
	ShardID  uint64     `json:"shard_id"`
	LeaderID uint64     `json:"leader_id"`
	Peers    []peerInfo `json:"peers"`
}

// peerInfo describes a single peer in a shard.
type peerInfo struct {
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
	IsLeader  bool   `json:"is_leader"`
}

// healthResponse is the JSON payload returned by GET /health.
type healthResponse struct {
	Status   string `json:"status"`
	IsLeader bool   `json:"is_leader"`
}

// capabilitiesResponse is the JSON payload returned by GET /capabilities.
type capabilitiesResponse struct {
	KV          bool `json:"kv"`
	MVCC        bool `json:"mvcc"`
	Transaction bool `json:"transaction"`
	Batch       bool `json:"batch"`
	Watch       bool `json:"watch"`
	Lease       bool `json:"lease"`
	Index       bool `json:"index"`
	TimeSeries  bool `json:"timeSeries"`
	Vector      bool `json:"vector"`
}

// drainResponse is the JSON payload returned by POST /drain.
type drainResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// kvListResponse is the JSON payload returned by GET /kv?prefix=X.
type kvListResponse struct {
	Keys []string `json:"keys"`
}

// kvScanResponse is the JSON payload returned by GET /kv/scan?prefix=X.
type kvScanResponse struct {
	Pairs map[string][]byte `json:"pairs"`
}

// okResponse is a simple JSON success response.
type okResponse struct {
	Status string `json:"status"`
}

// errorResponse is a simple JSON error response.
type errorResponse struct {
	Error string `json:"error"`
}

// membershipAddRequest is the JSON payload for POST /membership/add.
type membershipAddRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
}

// membershipRemoveRequest is the JSON payload for POST /membership/remove.
type membershipRemoveRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
}

// snapshotRequest is the JSON payload for POST /snapshot.
type snapshotRequest struct {
	ShardID uint64 `json:"shard_id"`
}

// ---------------------------------------------------------------------------
// KV method dispatch
// ---------------------------------------------------------------------------

// kvMethodHandler handles a KV request for a specific HTTP method.
type kvMethodHandler func(s *Server, w http.ResponseWriter, r *http.Request, key string)

// kvMethodHandlers maps HTTP methods to their KV handler functions.
var kvMethodHandlers = map[string]kvMethodHandler{
	http.MethodPut:    (*Server).handleKVPut,
	http.MethodGet:    (*Server).handleKVGet,
	http.MethodDelete: (*Server).handleKVDelete,
}

// ---------------------------------------------------------------------------
// Admin handlers
// ---------------------------------------------------------------------------

// handleStatus returns node and shard status information.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	info, err := s.adminSvc.Status(r.Context())
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, err)
		return
	}

	resp := statusResponse{
		NodeID:      info.NodeID,
		RaftAddress: info.RaftAddress,
		IsLeader:    info.IsLeader,
		LeaderID:    info.LeaderID,
		ShardCount:  info.ShardCount,
		IsDraining:  info.IsDraining,
		Shards:      info.Shards,
	}

	writeJSON(w, resp)
}

// handlePeers returns the peer list for all loaded shards.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	info, err := s.adminSvc.Peers(r.Context())
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, err)
		return
	}

	resp := peersResponse{
		Shards: make([]shardPeers, 0, len(info.Shards)),
	}

	for _, sp := range info.Shards {
		peers := make([]peerInfo, 0, len(sp.Peers))
		for _, p := range sp.Peers {
			peers = append(peers, peerInfo{
				ReplicaID: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			})
		}
		resp.Shards = append(resp.Shards, shardPeers{
			ShardID:  sp.ShardID,
			LeaderID: sp.LeaderID,
			Peers:    peers,
		})
	}

	writeJSON(w, resp)
}

// handleHealth returns a health check response. Returns 200 for "ok"
// status and 503 for any degraded or unhealthy state.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	info, err := s.adminSvc.Health(r.Context())
	if err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, err)
		return
	}

	if info.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	writeJSON(w, healthResponse{
		Status:   info.Status,
		IsLeader: info.Leader,
	})
}

// handleDrain transitions the host into drain mode.
func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	if err := s.adminSvc.Drain(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, drainResponse{Status: "drain_timeout", Error: err.Error()})
		return
	}
	writeJSON(w, drainResponse{Status: "drained"})
}

// handleCapabilities returns the compile-time feature set of the QRDB binary.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	caps, err := s.adminSvc.Capabilities(r.Context())
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, capabilitiesResponse{
		KV:          caps.KV,
		MVCC:        caps.MVCC,
		Transaction: caps.Transaction,
		Batch:       caps.Batch,
		Watch:       caps.Watch,
		Lease:       caps.Lease,
		Index:       caps.Index,
		TimeSeries:  caps.TimeSeries,
		Vector:      caps.Vector,
	})
}

// handleMetrics serves Prometheus metrics from the configured gatherer.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(s.gatherer, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// KV handlers
// ---------------------------------------------------------------------------

// handleKV routes KV requests by HTTP method using map-based dispatch.
func (s *Server) handleKV(w http.ResponseWriter, r *http.Request) {
	// Extract key from path: /kv/{key}
	key := strings.TrimPrefix(r.URL.Path, "/kv")
	key = strings.TrimPrefix(key, "/")

	// GET /kv or GET /kv?prefix=X with no key -> list operation.
	if r.Method == http.MethodGet && key == "" {
		s.handleKVList(w, r)
		return
	}

	handler, ok := kvMethodHandlers[r.Method]
	if !ok {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if key == "" && r.Method != http.MethodGet {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	handler(s, w, r, key)
}

// handleKVPut handles PUT /kv/{key} -- stores a key-value pair. When the
// lease-id query parameter is present and the MVCC service is configured,
// the value is stored with the specified lease attachment. When the local
// node is not the leader and leader forwarding is configured, the request
// is transparently proxied to the leader.
func (s *Server) handleKVPut(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx := r.Context()

	// Check for lease-id query parameter for PutWithLease.
	if leaseIDStr := r.URL.Query().Get("lease-id"); leaseIDStr != "" {
		mvccSvc := s.mvccSvcFor(w, ctx)
		if mvccSvc == nil {
			// mvccSvcFor already wrote the response (error or 501).
			if s.serviceRegistry == nil {
				writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "mvcc"})
			}
			return
		}
		leaseID, parseErr := strconv.ParseInt(leaseIDStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "lease-id", Err: parseErr})
			return
		}
		kv, putErr := mvccSvc.PutWithLease(ctx, key, body, leaseID)
		if putErr != nil {
			if s.tryForwardWrite(w, r, key, body, putErr) {
				return
			}
			writeErrorJSON(w, mapHTTPError(putErr), putErr)
			return
		}
		writeJSON(w, kvToResponse(kv))
		return
	}

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	if err := kvSvc.Put(ctx, key, body); err != nil {
		if s.tryForwardWrite(w, r, key, body, err) {
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &ProposeError{Err: err})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleKVGet handles GET /kv/{key} -- retrieves a value by key. When the
// revision=true query parameter is present, returns the full KeyValue with
// MVCC metadata. When at-revision=N is present, returns the key as it
// existed at that historical revision.
func (s *Server) handleKVGet(w http.ResponseWriter, r *http.Request, key string) {
	ctx := r.Context()
	q := r.URL.Query()

	// GET /kv/{key}?at-revision=N -- historical revision read.
	if atRevStr := q.Get("at-revision"); atRevStr != "" {
		mvccSvc := s.mvccSvcFor(w, ctx)
		if mvccSvc == nil {
			if s.serviceRegistry == nil {
				writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "mvcc"})
			}
			return
		}
		rev, parseErr := strconv.ParseInt(atRevStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "at-revision", Err: parseErr})
			return
		}
		kv, getErr := mvccSvc.GetAtRevision(ctx, key, rev)
		if getErr != nil {
			writeErrorJSON(w, mapHTTPError(getErr), getErr)
			return
		}
		writeJSON(w, kvToResponse(kv))
		return
	}

	// GET /kv/{key}?revision=true -- full KeyValue with MVCC metadata.
	if q.Get("revision") == "true" {
		mvccSvc := s.mvccSvcFor(w, ctx)
		if mvccSvc == nil {
			if s.serviceRegistry == nil {
				writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "mvcc"})
			}
			return
		}
		kv, getErr := mvccSvc.GetRevision(ctx, key)
		if getErr != nil {
			writeErrorJSON(w, mapHTTPError(getErr), getErr)
			return
		}
		writeJSON(w, kvToResponse(kv))
		return
	}

	// Default: simple byte value retrieval. When ?stale=true, reads from
	// the local state machine without Raft coordination for sub-millisecond
	// latency (may return stale data).
	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	var data []byte
	var err error
	if q.Get("stale") == "true" {
		data, err = kvSvc.GetStale(ctx, key)
	} else {
		data, err = kvSvc.Get(ctx, key)
	}
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &ReadError{Err: err})
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleKVDelete handles DELETE /kv/{key} -- removes a key. When the
// local node is not the leader and leader forwarding is configured, the
// request is transparently proxied to the leader.
func (s *Server) handleKVDelete(w http.ResponseWriter, r *http.Request, key string) {
	ctx := r.Context()
	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}
	if err := kvSvc.Delete(ctx, key); err != nil {
		if s.tryForwardWrite(w, r, key, nil, err) {
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &ProposeError{Err: err})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleKVList handles GET /kv?prefix=X -- lists keys by prefix. When
// ?stale=true, reads from the local state machine without Raft coordination
// for sub-millisecond latency (may return stale data).
func (s *Server) handleKVList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")

	ctx := r.Context()
	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	var keys []string
	var err error
	if q.Get("stale") == "true" {
		keys, err = kvSvc.ListStale(ctx, prefix)
	} else {
		keys, err = kvSvc.List(ctx, prefix)
	}
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, &ReadError{Err: err})
		return
	}

	writeJSON(w, kvListResponse{Keys: keys})
}

// handleKVScan handles GET /kv/scan?prefix=X -- returns key-value pairs
// matching the given prefix.
func (s *Server) handleKVScan(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	ctx := r.Context()
	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	pairs, err := kvSvc.Scan(ctx, prefix)
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, &ReadError{Err: err})
		return
	}

	writeJSON(w, kvScanResponse{Pairs: pairs})
}

// ---------------------------------------------------------------------------
// Membership and snapshot handlers
// ---------------------------------------------------------------------------

// handleMembershipAdd handles POST /membership/add.
func (s *Server) handleMembershipAdd(w http.ResponseWriter, r *http.Request) {
	var req membershipAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx := r.Context()
	if err := s.adminSvc.AddMember(ctx, req.ShardID, req.ReplicaID, req.Address); err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, &MembershipError{Op: "add", Err: err})
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleMembershipRemove handles POST /membership/remove.
func (s *Server) handleMembershipRemove(w http.ResponseWriter, r *http.Request) {
	var req membershipRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx := r.Context()
	if err := s.adminSvc.RemoveMember(ctx, req.ShardID, req.ReplicaID); err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, &MembershipError{Op: "remove", Err: err})
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleSnapshot handles POST /snapshot.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var req snapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx := r.Context()
	if err := s.adminSvc.RequestSnapshot(ctx, req.ShardID); err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, &SnapshotRequestError{Err: err})
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// ---------------------------------------------------------------------------
// Leader forwarding
// ---------------------------------------------------------------------------

// tryForwardWrite checks whether err is a NotLeaderError with a known
// leader address and forwards the write request to the leader's REST
// server. Returns true if forwarding was attempted (regardless of whether
// it succeeded), meaning the caller should not write any further response.
// Returns false if forwarding is not applicable.
func (s *Server) tryForwardWrite(w http.ResponseWriter, r *http.Request, key string, body []byte, err error) bool {
	if s.resolver == nil {
		return false
	}

	var nle *service.NotLeaderError
	if !errors.As(err, &nle) {
		return false
	}

	if nle.LeaderAddress == "" {
		slog.Warn("leader forwarding: leader address unknown",
			"shard_id", nle.ShardID,
		)
		return false
	}

	adminURL := s.resolver.ResolveAdminURL(nle.LeaderAddress)
	if adminURL == "" {
		slog.Warn("leader forwarding: no admin URL for leader",
			"leader_raft_addr", nle.LeaderAddress,
			"leader_id", nle.LeaderID,
		)
		return false
	}

	s.forwardToLeader(w, r, adminURL, key, body)
	return true
}

// forwardToLeader proxies the original HTTP request to the leader's REST
// server at the given base URL. It reconstructs the request path from the
// original method and key, copies the response status and body back.
// The X-Quicraft-Forwarded-By header is set to prevent infinite forwarding
// loops.
func (s *Server) forwardToLeader(w http.ResponseWriter, r *http.Request, adminURL, key string, body []byte) {
	// Prevent forwarding loops: if we already forwarded this request, stop.
	if r.Header.Get(headerForwardedBy) != "" {
		writeErrorJSON(w, http.StatusLoopDetected,
			&LeaderForwardLoopError{LeaderURL: adminURL})
		return
	}

	targetURL := adminURL + "/kv/" + key

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	fwdReq, reqErr := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyReader)
	if reqErr != nil {
		writeErrorJSON(w, http.StatusBadGateway,
			&LeaderForwardError{LeaderURL: adminURL, Err: reqErr})
		return
	}

	// Set the forwarded-by header using the host's Raft address when
	// available, otherwise use a placeholder.
	forwardedBy := "unknown"
	if s.host != nil {
		forwardedBy = s.host.RaftAddress()
	}
	fwdReq.Header.Set(headerForwardedBy, forwardedBy)

	resp, doErr := s.forwardClient.Do(fwdReq)
	if doErr != nil {
		writeErrorJSON(w, http.StatusBadGateway,
			&LeaderForwardError{LeaderURL: adminURL, Err: doErr})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy the leader's response headers and status back to the client.
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if _, copyErr := io.Copy(w, resp.Body); copyErr != nil {
		slog.Error("leader forwarding: error copying response body",
			"error", copyErr,
			"leader_url", targetURL,
		)
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// writeJSON encodes v as JSON and writes it to w with the
// application/json content type.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("rest: JSON encode error", "error", err)
	}
}

// writeErrorJSON writes a JSON error response with the given HTTP status
// code.
func writeErrorJSON(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if encErr := json.NewEncoder(w).Encode(errorResponse{Error: err.Error()}); encErr != nil {
		slog.Error("rest: JSON encode error", "error", encErr)
	}
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// mapHTTPError maps service-layer errors to the appropriate HTTP status code.
// Returns 500 Internal Server Error for unrecognized errors.
func mapHTTPError(err error) int {
	var knf *service.KeyNotFoundError
	var rce *service.RevisionCompactedError
	var rnf *service.RevisionNotFoundError
	var tc *service.TransactionConflictError
	var wc *service.WatchCanceledError
	var lnf *service.LeaseNotFoundError
	var le *service.LeaseExpiredError
	var lex *service.LeaseExistsError
	var be *service.BatchError
	var inf *service.IndexNotFoundError
	var ie *service.IndexExistsError
	var tse *service.TimeSeriesError
	var ce *service.ConsistencyError
	var nle *service.NotLeaderError
	var vnf *service.VectorNotFoundError
	var nsnf *service.NamespaceNotFoundError
	var nse *service.NamespaceExistsError
	var dme *service.DimensionMismatchError
	var ive *service.InvalidVectorError
	var iif *service.ImportInvalidFormatError
	var ivm *service.ImportInvalidValueModeError
	var imk *service.ImportMissingKeyFieldError
	var imv *service.ImportMissingValueFieldError
	var ipe *service.ImportParseError
	var iae *service.ImportArchiveError
	var ita *service.ImportTransactionAbortedError
	var dbae *database.AlreadyExistsError
	var dbnf *database.NotFoundError
	var dbine *database.InvalidNameError
	var dbiene *database.InvalidEngineError
	var dbibe *database.InvalidBackendError
	var cae *service.CollectionAlreadyExistsError
	var cnf *service.CollectionNotFoundError
	var cen *service.CollectionEmptyNameError
	var ced *service.CollectionEmptyDatabaseError
	var cis *service.CollectionInvalidIDStrategyError
	var cmf *service.CollectionMissingFieldHashError
	var cdnf *service.CollectionDocumentNotFoundError
	var cedc *service.CollectionEmptyDocumentError
	var cidi *service.CollectionInvalidDocumentIDError
	var cme *service.CollectionMetadataError
	var lhe *lock.LockHeldError
	var nlhe *lock.NotLockHolderError
	var lnfe *lock.LockNotFoundError
	var ragdnf *service.RAGDocumentNotFoundError
	var ragclosed *service.RAGServiceClosedError
	var tnf *TenantNotFoundError
	var dnf *DatabaseNotFoundError
	var bse *BarrierSealedError

	switch {
	case errors.As(err, &knf):
		return http.StatusNotFound
	case errors.As(err, &rce):
		return http.StatusRequestedRangeNotSatisfiable
	case errors.As(err, &rnf):
		return http.StatusNotFound
	case errors.As(err, &tc):
		return http.StatusConflict
	case errors.As(err, &wc):
		return http.StatusInternalServerError
	case errors.As(err, &lnf):
		return http.StatusNotFound
	case errors.As(err, &le):
		return http.StatusNotFound
	case errors.As(err, &lex):
		return http.StatusConflict
	case errors.As(err, &be):
		return http.StatusInternalServerError
	case errors.As(err, &inf):
		return http.StatusNotFound
	case errors.As(err, &ie):
		return http.StatusConflict
	case errors.As(err, &tse):
		return http.StatusInternalServerError
	case errors.As(err, &ce):
		return http.StatusPreconditionFailed
	case errors.As(err, &nle):
		return http.StatusServiceUnavailable
	case errors.As(err, &vnf):
		return http.StatusNotFound
	case errors.As(err, &nsnf):
		return http.StatusNotFound
	case errors.As(err, &nse):
		return http.StatusConflict
	case errors.As(err, &dme):
		return http.StatusBadRequest
	case errors.As(err, &ive):
		return http.StatusBadRequest
	case errors.As(err, &iif):
		return http.StatusBadRequest
	case errors.As(err, &ivm):
		return http.StatusBadRequest
	case errors.As(err, &imk):
		return http.StatusBadRequest
	case errors.As(err, &imv):
		return http.StatusBadRequest
	case errors.As(err, &ipe):
		return http.StatusBadRequest
	case errors.As(err, &iae):
		return http.StatusBadRequest
	case errors.As(err, &ita):
		return http.StatusConflict
	case errors.As(err, &dbae):
		return http.StatusConflict
	case errors.As(err, &dbnf):
		return http.StatusNotFound
	case errors.As(err, &dbine):
		return http.StatusBadRequest
	case errors.As(err, &dbiene):
		return http.StatusBadRequest
	case errors.As(err, &dbibe):
		return http.StatusBadRequest
	case errors.As(err, &cae):
		return http.StatusConflict
	case errors.As(err, &cnf):
		return http.StatusNotFound
	case errors.As(err, &cen):
		return http.StatusBadRequest
	case errors.As(err, &ced):
		return http.StatusBadRequest
	case errors.As(err, &cis):
		return http.StatusBadRequest
	case errors.As(err, &cmf):
		return http.StatusBadRequest
	case errors.As(err, &cdnf):
		return http.StatusNotFound
	case errors.As(err, &cedc):
		return http.StatusBadRequest
	case errors.As(err, &cidi):
		return http.StatusBadRequest
	case errors.As(err, &cme):
		return http.StatusInternalServerError
	case errors.As(err, &lhe):
		return http.StatusConflict
	case errors.As(err, &nlhe):
		return http.StatusForbidden
	case errors.As(err, &lnfe):
		return http.StatusNotFound
	case errors.As(err, &ragdnf):
		return http.StatusNotFound
	case errors.As(err, &ragclosed):
		return http.StatusServiceUnavailable
	case errors.As(err, &tnf):
		return http.StatusNotFound
	case errors.As(err, &dnf):
		return http.StatusNotFound
	case errors.As(err, &bse):
		return http.StatusServiceUnavailable
	default:
		// Allow build-tagged packages to extend the error mapping.
		if ragErrorMapperFn != nil {
			if code, ok := ragErrorMapperFn(err); ok {
				return code
			}
		}
		return http.StatusInternalServerError
	}
}

// ragErrorMapperFn is optionally populated by a build-tagged init() to map
// RAG-specific typed errors to HTTP status codes without creating a build
// dependency on qrdb_vector types in this file.
var ragErrorMapperFn func(err error) (int, bool)

// ---------------------------------------------------------------------------
// KeyValue JSON response
// ---------------------------------------------------------------------------

// kvResponse is the JSON representation of an MVCC key-value pair.
type kvResponse struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// kvToResponse converts a kv.KeyValue to its JSON response representation.
// Key and Value are encoded as base64 via the standard JSON string encoding
// of []byte.
func kvToResponse(kv *kv.KeyValue) *kvResponse {
	if kv == nil {
		return nil
	}
	return &kvResponse{
		Key:            string(kv.Key),
		Value:          string(kv.Value),
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		LeaseID:        kv.LeaseID,
	}
}

// kvsToResponse converts a slice of kv.KeyValue to JSON response format.
func kvsToResponse(kvs []*kv.KeyValue) []*kvResponse {
	resp := make([]*kvResponse, len(kvs))
	for i, v := range kvs {
		resp[i] = kvToResponse(v)
	}
	return resp
}

// ---------------------------------------------------------------------------
// fetchJSON
// ---------------------------------------------------------------------------

// fetchJSON issues an HTTP GET to the given URL and decodes the JSON
// response into dst. The caCertPool is used to verify the server's TLS
// certificate; when nil the system pool is used.
func fetchJSON(url string, dst any, caCertPool *x509.CertPool, clientCerts ...tls.Certificate) error {
	tlsCfg := &tls.Config{
		RootCAs:    caCertPool,
		MinVersion: tls.VersionTLS13,
	}
	if len(clientCerts) > 0 {
		tlsCfg.Certificates = clientCerts
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   tlsCfg,
			ForceAttemptHTTP2: true,
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return &FetchError{URL: url, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return &FetchResponseError{URL: url, StatusCode: resp.StatusCode}
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &FetchError{URL: url, Err: err}
	}

	return nil
}
