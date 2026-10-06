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
	"context"
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

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// apiRequestTimeout bounds every KV, admin, cert, role, and user endpoint so
// a stalled Raft SyncRead never hangs the GUI. 5s is generous — operations on
// a healthy cluster return in milliseconds; failure must surface quickly.
const apiRequestTimeout = 5 * time.Second

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
	Status             string `json:"status"`
	IsLeader           bool   `json:"is_leader"`
	DiagnosticsDropped uint64 `json:"diagnostics_dropped"`
}

// capabilitiesResponse is the JSON payload returned by GET /capabilities.
type capabilitiesResponse struct {
	Mode        string `json:"mode"`
	KV          bool   `json:"kv"`
	MVCC        bool   `json:"mvcc"`
	Transaction bool   `json:"transaction"`
	Batch       bool   `json:"batch"`
	Watch       bool   `json:"watch"`
	Lease       bool   `json:"lease"`
	Lock        bool   `json:"lock"`
	Index       bool   `json:"index"`
	Cluster     bool   `json:"cluster"`
	Backup      bool   `json:"backup"`
	Rebalance   bool   `json:"rebalance"`
	TimeSeries  bool   `json:"timeSeries"`
	Vector      bool   `json:"vector"`
	SQL         bool   `json:"sql"`
	MySQL       bool   `json:"mysql"`
	PostgreSQL  bool   `json:"postgresql"`
	Etcd        bool   `json:"etcd"`
	FTS         bool   `json:"fts"`

	UnencryptedAtRest []string `json:"unencryptedAtRest,omitempty"`
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

	// Code and Metadata carry a replica refusal's structured form
	// (replicaforward.Refusal) -- NOT_A_REPLICA with the replica set,
	// NOT_LEADER with the leader -- so a forwarding node classifies the
	// answer, and a client redials, without parsing Error.
	Code     string            `json:"code,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// membershipAddRequest is the JSON payload for POST /membership/add.
type membershipAddRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
	// Identity is the optional operator-supplied member identity; see
	// service.AdminServicer.AddMember.
	Identity string `json:"identity,omitempty"`
}

// membershipRemoveRequest is the JSON payload for POST /membership/remove.
type membershipRemoveRequest struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
}

// membershipPromoteRequest is the JSON payload for POST /membership/promote.
type membershipPromoteRequest struct {
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
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	info, err := s.adminSvc.Status(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	info, err := s.adminSvc.Peers(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	info, err := s.adminSvc.Health(ctx)
	if err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, err)
		return
	}

	if info.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	writeJSON(w, healthResponse{
		Status:             info.Status,
		IsLeader:           info.Leader,
		DiagnosticsDropped: info.DiagnosticsDropped,
	})
}

// handleDrain transitions the host into drain mode.
func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.adminSvc.Drain(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, drainResponse{Status: "drain_timeout", Error: err.Error()})
		return
	}
	writeJSON(w, drainResponse{Status: "drained"})
}

// handleCapabilities returns the effective feature set of the node
// (build tags x run mode x config).
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	caps, err := s.adminSvc.Capabilities(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, capabilitiesResponse{
		Mode:        caps.Mode,
		KV:          caps.KV,
		MVCC:        caps.MVCC,
		Transaction: caps.Transaction,
		Batch:       caps.Batch,
		Watch:       caps.Watch,
		Lease:       caps.Lease,
		Lock:        caps.Lock,
		Index:       caps.Index,
		Cluster:     caps.Cluster,
		Backup:      caps.Backup,
		Rebalance:   caps.Rebalance,
		TimeSeries:  caps.TimeSeries,
		Vector:      caps.Vector,
		SQL:         caps.SQL,
		MySQL:       caps.MySQL,
		PostgreSQL:  caps.PostgreSQL,
		Etcd:        caps.Etcd,
		FTS:         caps.FTS,

		UnencryptedAtRest: caps.UnencryptedAtRest,
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
//
// Optional query parameter ?write_mode=sync|wal_durable|batched selects the
// write durability mode; absent/empty preserves existing behavior.
func (s *Server) handleKVPut(w http.ResponseWriter, r *http.Request, key string) {
	body, ok := readBoundedBody(w, r, maxKVPutRequestBytes)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	q := r.URL.Query()

	// Check for lease-id query parameter for PutWithLease.
	if leaseIDStr := q.Get("lease-id"); leaseIDStr != "" {
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

		mode, modeSpecified, modeOK := parseWriteModeParam(w, q)
		if !modeOK {
			return
		}

		var kv *service.KeyValue
		var putErr error
		if modeSpecified {
			kv, putErr = mvccSvc.PutWithLeaseAndMode(ctx, key, body, leaseID, mode)
		} else {
			kv, putErr = mvccSvc.PutWithLease(ctx, key, body, leaseID)
		}
		if putErr != nil {
			if s.tryForwardWrite(w, r, body, putErr) {
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

	mode, modeSpecified, modeOK := parseWriteModeParam(w, q)
	if !modeOK {
		return
	}

	var putErr error
	if modeSpecified {
		putErr = kvSvc.PutWithMode(ctx, key, body, mode)
	} else {
		putErr = kvSvc.Put(ctx, key, body)
	}
	if putErr != nil {
		if s.tryForwardWrite(w, r, body, putErr) {
			return
		}
		writeProposeFailure(w, putErr)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleKVGet handles GET /kv/{key} -- retrieves a value by key. When the
// revision=true query parameter is present, returns the full KeyValue with
// MVCC metadata. When at-revision=N is present, returns the key as it
// existed at that historical revision.
//
// Optional query parameter ?consistency=strong|linearizable|eventual selects
// the read consistency level.
func (s *Server) handleKVGet(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

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

		consistOpts, consistSpecified, consistOK := parseConsistencyParam(w, q)
		if !consistOK {
			return
		}

		var kv *service.KeyValue
		var getErr error
		if consistSpecified {
			kv, getErr = mvccSvc.GetAtRevisionWithConsistency(ctx, key, rev, consistOpts)
		} else {
			kv, getErr = mvccSvc.GetAtRevision(ctx, key, rev)
		}
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

		consistOpts, consistSpecified, consistOK := parseConsistencyParam(w, q)
		if !consistOK {
			return
		}

		var kv *service.KeyValue
		var getErr error
		if consistSpecified {
			kv, getErr = mvccSvc.GetRevisionWithConsistency(ctx, key, consistOpts)
		} else {
			kv, getErr = mvccSvc.GetRevision(ctx, key)
		}
		if getErr != nil {
			writeErrorJSON(w, mapHTTPError(getErr), getErr)
			return
		}
		writeJSON(w, kvToResponse(kv))
		return
	}

	// Default: simple byte value retrieval. Consistency is controlled via
	// ?consistency=.
	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	consistOpts, consistSpecified, consistOK := parseConsistencyParam(w, q)
	if !consistOK {
		return
	}

	var data []byte
	var err error
	switch {
	case consistSpecified && consistOpts.Consistency == consistency.Eventual:
		data, err = kvSvc.GetStale(ctx, key)
	case consistSpecified:
		data, err = kvSvc.GetWithConsistency(ctx, key, consistOpts)
	default:
		data, err = kvSvc.Get(ctx, key)
	}
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		writeReadFailure(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- the body is an opaque KV value served as octet-stream,
	// never interpolated into a document; the value is returned verbatim by
	// contract, so escaping it would corrupt it.
	_, _ = w.Write(data)
}

// handleKVDelete handles DELETE /kv/{key} -- removes a key. When the
// local node is not the leader and leader forwarding is configured, the
// request is transparently proxied to the leader.
//
// Optional query parameter ?write_mode=sync|wal_durable|batched selects the
// write durability mode; absent/empty preserves existing behavior.
func (s *Server) handleKVDelete(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	mode, modeSpecified, modeOK := parseWriteModeParam(w, r.URL.Query())
	if !modeOK {
		return
	}

	var err error
	if modeSpecified {
		err = kvSvc.DeleteWithMode(ctx, key, mode)
	} else {
		err = kvSvc.Delete(ctx, key)
	}
	if err != nil {
		if s.tryForwardWrite(w, r, nil, err) {
			return
		}
		writeProposeFailure(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleKVList handles GET /kv?prefix=X -- lists keys by prefix. Consistency
// is controlled via ?consistency=strong|linearizable|eventual.
func (s *Server) handleKVList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	consistOpts, consistSpecified, consistOK := parseConsistencyParam(w, q)
	if !consistOK {
		return
	}

	var keys []string
	var err error
	switch {
	case consistSpecified && consistOpts.Consistency == consistency.Eventual:
		keys, err = kvSvc.ListStale(ctx, prefix)
	case consistSpecified:
		keys, err = kvSvc.ListWithConsistency(ctx, prefix, consistOpts)
	default:
		keys, err = kvSvc.List(ctx, prefix)
	}
	if err != nil {
		writeReadFailure(w, err)
		return
	}

	writeJSON(w, kvListResponse{Keys: keys})
}

// handleKVScan handles GET /kv/scan?prefix=X -- returns key-value pairs
// matching the given prefix. Consistency is controlled via
// ?consistency=strong|linearizable|eventual.
func (s *Server) handleKVScan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		return
	}

	consistOpts, consistSpecified, consistOK := parseConsistencyParam(w, q)
	if !consistOK {
		return
	}

	var pairs map[string][]byte
	var err error
	switch {
	case consistSpecified && consistOpts.Consistency == consistency.Eventual:
		pairs, err = kvSvc.ScanStale(ctx, prefix)
	case consistSpecified:
		pairs, err = kvSvc.ScanWithConsistency(ctx, prefix, consistOpts)
	default:
		pairs, err = kvSvc.Scan(ctx, prefix)
	}
	if err != nil {
		writeReadFailure(w, err)
		return
	}

	writeJSON(w, kvScanResponse{Pairs: pairs})
}

// ---------------------------------------------------------------------------
// Membership and snapshot handlers
// ---------------------------------------------------------------------------

// membershipChangeStatus is the HTTP status for a failed membership add: the
// status pkg/api/errclass gives a member-identity refusal or a feature-gate
// admission refusal when the failure is one of those, else 500. Deliberately
// narrower than a blanket mapHTTPError(err): a membership change reaches
// go-quicraft directly, and errclass's Unclassified default (500) is the
// right answer for whatever else that surface can return, not a class this
// function would otherwise have to guess at.
func membershipChangeStatus(err error) int {
	var mie *service.MemberIdentityError
	if errors.As(err, &mie) {
		return mapHTTPError(err)
	}
	var ure *featuregate.UnsupportedReplicaError
	if errors.As(err, &ure) {
		return mapHTTPError(err)
	}
	var noe *service.MemberNotObserverError
	if errors.As(err, &noe) {
		return mapHTTPError(err)
	}
	return http.StatusInternalServerError
}

// handleMembershipAdd handles POST /membership/add. When the local node is
// not the shard's Raft leader and leader forwarding is configured, the
// request is transparently proxied to the leader (see tryForwardWrite),
// mirroring the KV PUT/DELETE and database-scale handlers. The body is
// buffered up front (rather than decoded directly off r.Body) so the
// original bytes remain available for forwarding.
func (s *Server) handleMembershipAdd(w http.ResponseWriter, r *http.Request) {
	body, ok := readBoundedBody(w, r, maxControlPlaneRequestBytes)
	if !ok {
		return
	}

	var req membershipAddRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.adminSvc.AddMember(ctx, req.ShardID, req.ReplicaID, req.Address, req.Identity); err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, membershipChangeStatus(err), &MembershipError{Op: "add", Err: err})
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleMembershipAddObserver handles POST /membership/add-observer: adds
// the replica as a non-voting observer rather than a voter (see
// service.ObserverMemberAdder), promoted later with POST /membership/promote.
// Leader forwarding follows the same pattern as handleMembershipAdd.
func (s *Server) handleMembershipAddObserver(w http.ResponseWriter, r *http.Request) {
	adder, ok := s.adminSvc.(service.ObserverMemberAdder)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ClusterJoinRequestReviewUnsupportedError{})
		return
	}

	body, ok := readBoundedBody(w, r, maxControlPlaneRequestBytes)
	if !ok {
		return
	}

	var req membershipAddRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := adder.AddObserverMember(ctx, req.ShardID, req.ReplicaID, req.Address, req.Identity); err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, membershipChangeStatus(err), &MembershipError{Op: "add-observer", Err: err})
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleMembershipRemove handles POST /membership/remove. When the local
// node is not the shard's Raft leader and leader forwarding is configured,
// the request is transparently proxied to the leader (see tryForwardWrite),
// mirroring the KV PUT/DELETE and database-scale handlers. The body is
// buffered up front (rather than decoded directly off r.Body) so the
// original bytes remain available for forwarding.
func (s *Server) handleMembershipRemove(w http.ResponseWriter, r *http.Request) {
	body, ok := readBoundedBody(w, r, maxControlPlaneRequestBytes)
	if !ok {
		return
	}

	var req membershipRemoveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.adminSvc.RemoveMember(ctx, req.ShardID, req.ReplicaID); err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &MembershipError{Op: "remove", Err: err})
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleMembershipPromote handles POST /membership/promote, promoting a
// committed observer to a voter. Leader forwarding follows the same pattern
// as handleMembershipAdd and handleMembershipRemove.
func (s *Server) handleMembershipPromote(w http.ResponseWriter, r *http.Request) {
	promoter, ok := s.adminSvc.(service.MemberPromoter)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.MemberPromotionUnsupportedError{})
		return
	}

	body, ok := readBoundedBody(w, r, maxControlPlaneRequestBytes)
	if !ok {
		return
	}

	var req membershipPromoteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := promoter.PromoteMember(ctx, req.ShardID, req.ReplicaID); err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, membershipChangeStatus(err), &MembershipError{Op: "promote", Err: err})
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

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

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
// Returns false if forwarding is not applicable. body is the original
// request body (already drained from r.Body by the caller); it is
// forwarded as-is since it cannot be re-read from r.
func (s *Server) tryForwardWrite(w http.ResponseWriter, r *http.Request, body []byte, err error) bool {
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

	s.forwardToLeader(w, r, adminURL, body)
	return true
}

// forwardToLeader proxies the original HTTP request to the leader's REST
// server at the given base URL, preserving the original method, path, and
// query string (so this works for any write endpoint -- /kv/{key}, database
// scale, etc. -- not just KV), and copies the response status and body
// back. The X-Quicraft-Forward-Hops header carries a bounded hop counter
// (see maxForwardHops) so a genuine forwarding loop still terminates while
// legitimate multi-hop chains (e.g. a non-member entry node forwarding to a
// guessed shard member that is itself not the leader) can complete.
func (s *Server) forwardToLeader(w http.ResponseWriter, r *http.Request, adminURL string, body []byte) {
	// Bound forwarding chains: reject once the request has already taken
	// maxForwardHops hops, rather than capping at a single hop.
	hops := forwardHopCount(r)
	if hops >= maxForwardHops {
		writeErrorJSON(w, http.StatusLoopDetected,
			&LeaderForwardLoopError{LeaderURL: adminURL})
		return
	}

	targetURL := adminURL + r.URL.RequestURI()

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

	// Copy the original request headers so routing/content semantics survive
	// the hop -- in particular the X-QRDB-Database scope header the leader's
	// scope middleware reads to target the right database, plus Content-Type
	// and any consistency/write-mode headers. Control headers set below
	// (forwarded-by, on-behalf assertion) override any client-supplied value.
	for k, vals := range r.Header {
		for _, v := range vals {
			fwdReq.Header.Add(k, v)
		}
	}

	// Strip any client-supplied control headers so the forwarding node is the
	// SOLE source of the loop guard and the on-behalf assertion. A client can
	// never smuggle a forged assertion (or a signer's assertion) through the
	// proxy; the leader additionally re-verifies the assertion against the mTLS
	// peer, but stripping here keeps the forwarder the single trusted origin.
	fwdReq.Header.Del(headerForwardedBy)
	fwdReq.Header.Del(headerForwardHops)
	fwdReq.Header.Del(forward.HeaderAssertion)
	fwdReq.Header.Del(forward.HeaderSignature)

	// Set the forwarded-by header using the host's Raft address when
	// available, otherwise use a placeholder.
	forwardedBy := "unknown"
	if s.host != nil {
		forwardedBy = s.host.RaftAddress()
	}
	fwdReq.Header.Set(headerForwardedBy, forwardedBy)
	fwdReq.Header.Set(headerForwardHops, strconv.Itoa(hops+1))

	// Attach a signed on-behalf-of assertion carrying the ORIGINAL caller's
	// identity so the leader re-authorizes as the caller rather than as this
	// forwarding node (whose node certificate carries no user roles). Skipped
	// when there is no authenticated caller in context (auth-disabled
	// deployments) or when this node has no signing identity/node id -- except
	// for a holder caller, which is refused rather than forwarded unsigned.
	if err := s.attachOnBehalfAssertion(fwdReq, r, body); err != nil {
		// The client gets the fixed message only; the cause, which may
		// describe this node's signing key, is logged here.
		slog.Error("leader forwarding: refusing to forward a holder request unsigned",
			"leader", adminURL, "error", errors.Unwrap(err))
		writeErrorJSON(w, http.StatusBadGateway,
			&LeaderForwardError{LeaderURL: adminURL, Err: err})
		return
	}

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

// forwardHopCount returns the number of leader-forwarding hops already
// applied to r, as carried by headerForwardHops. A missing, empty, or
// malformed header is treated as zero hops -- i.e. this request has not yet
// been forwarded.
func forwardHopCount(r *http.Request) int {
	v := r.Header.Get(headerForwardHops)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// attachOnBehalfAssertion signs a request-bound CallerAssertion for the
// authenticated caller in r's context and sets the on-behalf headers on the
// outbound forwarded request fwdReq. body is the exact request body being
// forwarded (nil for bodyless requests). It is a no-op -- leaving the request
// to be authenticated as the forwarding node -- when there is no authenticated
// caller in context, when this node has no signing identity, or when signing
// fails (the failure is logged; the request still forwards and will simply be
// authorized as the node, preserving prior behavior for non-RBAC paths).
//
// A HOLDER caller (auth.IsHolderIdentity) is the exception, and the only case
// that returns an error: a holder request forwarded without an assertion would
// be authorized as the forwarding node, so when the assertion cannot be
// attached it returns *HolderForwardUnsignedError and the request is not
// forwarded. The assertion it does attach carries the holder profile, so the
// leader keeps the identity a holder.
func (s *Server) attachOnBehalfAssertion(fwdReq, r *http.Request, body []byte) error {
	caller := auth.IdentityFromContext(r.Context())
	if caller == nil || caller.Subject == "" {
		return nil
	}
	holder := auth.IsHolderIdentity(caller)
	if s.forwardSigner == nil || s.forwardNodeID == "" {
		if holder {
			return &HolderForwardUnsignedError{Subject: caller.Subject}
		}
		return nil
	}

	assertion := forward.NewCallerAssertion(
		caller.Subject,
		caller.TenantID,
		r.Method,
		r.URL.RequestURI(),
		forward.BodyDigest(body),
		s.forwardNodeID,
		time.Now(),
		forward.DefaultTTL,
	)
	assertion.HolderProfile = caller.Metadata[auth.HolderProfileMetadataKey]

	assertionB64, sigB64, err := forward.Sign(assertion, s.forwardSigner)
	if err != nil {
		slog.Error("leader forwarding: failed to sign on-behalf assertion",
			"error", err,
			"subject", caller.Subject,
			"tenant", caller.TenantID,
		)
		if holder {
			return &HolderForwardUnsignedError{Subject: caller.Subject, Err: err}
		}
		return nil
	}

	fwdReq.Header.Set(forward.HeaderAssertion, assertionB64)
	fwdReq.Header.Set(forward.HeaderSignature, sigB64)
	return nil
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

// writeProposeFailure reports a write that did not reach the cluster.
//
// It exists for two cases, both of which a blanket 500 describes wrongly.
//
// Since the encoders began refusing a field longer than its length field can
// describe (go-qrdb#52), an oversized key fails on this path without a proposal
// ever being made — the caller's input is wrong, not the cluster. A 500 for
// that tells the caller the cluster is broken, alerts an operator, and invites
// a retry of an input that can never succeed. A closed store is the mirror
// image: the request was fine and the node is going away (go-qrdb#371).
//
// Deliberately NOT `mapHTTPError(err)` wholesale: that would also reclassify
// the other errors reaching this path, and this is not the change to do it in.
// A non-forwardable NotLeaderError, for instance, would become 503 — arguably
// better, but a behaviour change nobody asked for, and it is pinned at 500 by
// existing tests. Everything except those two keeps exactly the status and the
// ProposeError wrapper it had.
func writeProposeFailure(w http.ResponseWriter, err error) {
	var encodeErr *service.EncodeRequestError
	if errors.As(err, &encodeErr) {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	// A write that met a closed store is the node going away under the
	// request. The STATUS comes from mapHTTPError rather than being written
	// again here: the same condition reaches this file through five other
	// paths, and a number repeated per path is a number that disagrees with
	// itself (go-qrdb#371).
	if errors.Is(err, storage.ErrStorageClosed) {
		writeErrorJSON(w, mapHTTPError(err), &ProposeError{Err: err})
		return
	}
	// A write the state machine refused because the database's drop fence
	// stood at its log position is the database being dropped, not a server
	// fault: 409, the answer every other transport and every resolve-time
	// refusal of the same condition gives.
	var dropping *database.DroppingError
	if errors.As(err, &dropping) {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeErrorJSON(w, http.StatusInternalServerError, &ProposeError{Err: err})
}

// writeReadFailure reports a read that could not be served.
//
// It is writeProposeFailure's counterpart on the other verb, and it exists for
// one case, the same one: a read that met a closed store. go-qrdb#371's whole
// argument — "500 tells a client the request is poison and invites a retry that
// can never work" — is as true of a read as of a write, so answering PUT /kv/k
// with 503 and GET /kv/k with 500 would leave the verb asymmetry the issue was
// filed to remove.
//
// Deliberately NOT `mapHTTPError(err)` wholesale, measured rather than assumed.
// KVService.translateReadError yields exactly three things: KeyNotFoundError,
// NotLeaderError, or ReadError wrapping whatever the host returned. Of those,
// handleKVGet answers KeyNotFoundError itself before reaching here and a
// list or scan cannot produce one, so the only status the wholesale form would
// additionally change is NotLeaderError, from 500 to 503. writeProposeFailure
// pins that error at 500 on the write path with a written reason, so moving it
// on reads alone would create a fresh instance of exactly the asymmetry this
// function removes. Everything except the closed store keeps the status and
// the ReadError wrapper it had.
func writeReadFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrStorageClosed) {
		writeErrorJSON(w, mapHTTPError(err), &ReadError{Err: err})
		return
	}
	writeErrorJSON(w, http.StatusInternalServerError, &ReadError{Err: err})
}

// writeErrorJSON writes a JSON error response with the given HTTP status
// code.
func writeErrorJSON(w http.ResponseWriter, code int, err error) {
	setRetryAfter(w, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := errorResponse{Error: err.Error()}
	if reason, md, ok := replicaforward.Refusal(err); ok {
		body.Code, body.Metadata = reason, md
	}
	if encErr := json.NewEncoder(w).Encode(body); encErr != nil {
		slog.Error("rest: JSON encode error", "error", encErr)
	}
}

// setRetryAfter sets the standard Retry-After header (RFC 9110 §10.2.3,
// delay-seconds) when err says how long the caller must wait
// (errclass.RetryAfter: a throttled ceremony-sign-csr, a throttled quorum
// request, an SO PIN lockout). The delay is rounded up, and never below one
// second, so a caller that honours it is never early. It must run before the
// status is written: net/http drops headers set after WriteHeader.
func setRetryAfter(w http.ResponseWriter, err error) {
	wait, ok := errclass.RetryAfter(err)
	if !ok {
		return
	}
	w.Header().Set("Retry-After", strconv.FormatInt(authmw.RetryAfterSeconds(wait), 10))
}

// ---------------------------------------------------------------------------
// KeyValue JSON response
// ---------------------------------------------------------------------------

// kvResponse is the JSON representation of an MVCC key-value pair. Key and
// Value are []byte so encoding/json encodes them as base64 (RFC 4648),
// preserving arbitrary binary content byte-for-byte. KV values are raw
// bytes accepted from a PUT request body; encoding them as a JSON string
// instead would replace invalid UTF-8 sequences with U+FFFD and corrupt
// non-UTF-8 values on GET/scan/watch.
type kvResponse struct {
	Key            []byte `json:"key"`
	Value          []byte `json:"value"`
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
		Key:            kv.Key,
		Value:          kv.Value,
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
