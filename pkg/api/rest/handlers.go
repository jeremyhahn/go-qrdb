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
	"crypto"
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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
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
	Status   string `json:"status"`
	IsLeader bool   `json:"is_leader"`
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
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	info, err := s.adminSvc.Status(ctx)
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
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	info, err := s.adminSvc.Peers(ctx)
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
		Status:   info.Status,
		IsLeader: info.Leader,
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
		writeErrorJSON(w, http.StatusInternalServerError, err)
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
// the read consistency level. The legacy ?stale=true is equivalent to
// ?consistency=eventual. When both are given, ?consistency= wins.
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
	// ?consistency= or the legacy ?stale=true shorthand.
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
		// Both ?consistency=eventual and legacy ?stale=true take this path.
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
// is controlled via ?consistency=strong|linearizable|eventual or the legacy
// ?stale=true shorthand (equivalent to eventual).
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
// ?consistency=strong|linearizable|eventual or the legacy ?stale=true
// shorthand (equivalent to eventual).
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

	if err := s.adminSvc.AddMember(ctx, req.ShardID, req.ReplicaID, req.Address); err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &MembershipError{Op: "add", Err: err})
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
	// deployments) or when this node has no signing identity/node id.
	s.attachOnBehalfAssertion(fwdReq, r, body)

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
func (s *Server) attachOnBehalfAssertion(fwdReq, r *http.Request, body []byte) {
	if s.forwardSigner == nil || s.forwardNodeID == "" {
		return
	}
	caller := auth.IdentityFromContext(r.Context())
	if caller == nil || caller.Subject == "" {
		return
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

	assertionB64, sigB64, err := forward.Sign(assertion, s.forwardSigner)
	if err != nil {
		slog.Error("leader forwarding: failed to sign on-behalf assertion",
			"error", err,
			"subject", caller.Subject,
			"tenant", caller.TenantID,
		)
		return
	}

	fwdReq.Header.Set(forward.HeaderAssertion, assertionB64)
	fwdReq.Header.Set(forward.HeaderSignature, sigB64)
}

// deriveForwardIdentity extracts the on-behalf signing key and this node's
// SPIFFE node id from the node's own TLS identity certificate. It returns
// (nil, "") when no usable identity is present so the caller falls back to
// node-identity authorization on forwarded requests (unchanged behavior). Both
// the static Certificates slice and a GetClientCertificate callback are probed
// so the derivation works regardless of how the transport TLS was provisioned.
func deriveForwardIdentity(tlsCfg *tls.Config) (crypto.Signer, string) {
	if tlsCfg == nil {
		return nil, ""
	}

	leaf := leafCertificate(tlsCfg)
	if leaf == nil {
		return nil, ""
	}

	signer, ok := leaf.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, ""
	}

	parsed := leaf.Leaf
	if parsed == nil {
		if len(leaf.Certificate) == 0 {
			return nil, ""
		}
		var perr error
		parsed, perr = x509.ParseCertificate(leaf.Certificate[0])
		if perr != nil {
			return nil, ""
		}
	}

	// The node id is not trust-domain-pinned here: it merely stamps the
	// assertion's ForwarderNodeID, which the leader re-checks against the
	// mTLS peer certificate it actually verified against the cluster CA.
	nodeID, err := spiffe.NodeFromCert(parsed, "")
	if err != nil {
		return signer, ""
	}
	return signer, nodeID.NodeID
}

// leafCertificate returns the node's own TLS identity certificate from cfg,
// preferring the static Certificates slice and falling back to a
// GetClientCertificate callback. It returns nil when neither yields a
// certificate with a usable private key.
func leafCertificate(cfg *tls.Config) *tls.Certificate {
	if len(cfg.Certificates) > 0 && cfg.Certificates[0].PrivateKey != nil {
		return &cfg.Certificates[0]
	}
	if cfg.GetClientCertificate != nil {
		if cert, err := cfg.GetClientCertificate(&tls.CertificateRequestInfo{}); err == nil &&
			cert != nil && cert.PrivateKey != nil {
			return cert
		}
	}
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
		writeErrorJSON(w, http.StatusBadRequest, err)
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
	var iite *service.ImportInputTooLargeError
	var iaete *service.ImportArchiveExpandedTooLargeError
	var itme *service.ImportTooManyEntriesError
	var itmr *service.ImportTooManyRowsError
	var ipte *service.ImportProposalTooLargeError
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
	var cde *service.CollectionDocumentExistsError
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
	var ibu *IsolationBackendUnavailableError
	var smnc *service.SMNotConfiguredError
	var cue *service.CapabilityUnsupportedError
	var qee *quota.ExceededError
	var qent *quota.EntitlementError
	var sqleq *service.SQLEmptyQueryError
	var sqled *service.SQLEmptyDatabaseError
	var sqlex *service.SQLExecutionError
	var itce *tenant.InvalidTenantIDCharError
	var irt *service.InvalidRaftTopologyError
	var rdu *service.RaftDistributedUnavailableError
	var tqee *tenant.QuotaExceededError
	var infe *placement.InsufficientNodesError
	var rfe *service.ReplicaFloorError
	var ndre *service.NotDedicatedRaftDatabaseError
	var slte *service.ShardLeaderTimeoutError
	var utoe *service.UnsupportedTransactionOpError
	var cso *service.CompactSystemOnlyError
	var tade *database.TenantAccessDeniedError
	var mbr *service.MVCCBackendRequiredError
	var rtl *RequestTooLargeError
	var ptl *PayloadTooLargeError
	var ktl *KeyTooLargeError
	var rike *service.RestoreInternalKeyError
	var rciu *service.RestoreChangeIndexUnavailableError
	var rnre *service.RestoreNilRequestError
	var rrie *service.RestoreRevisionInvalidError
	var rrfe *service.RestoreRevisionFutureError
	var rsee *service.RestoreScopeEmptyError
	var rsae *service.RestoreScopeAmbiguousError
	var reke *service.RestoreEmptyKeyError
	var rste *service.RestoreScopeTooLargeError
	var rupe *service.RestoreUnboundedPrefixError
	var rae *service.RestoreApplyError

	var sni *ServiceNotImplementedError
	var ere *service.EncodeRequestError

	switch {
	case errors.As(err, &sni):
		return http.StatusNotImplemented
	// A table created by v0.20.0 through v0.21.0 predates the current key
	// layout and is refused until it is dropped and re-created
	// (go-qrdb#547): 412, the precondition the operator must restore. Near
	// the top so no wrapper's arm below claims it as a 500.
	case service.IsTableLayoutUnsupported(err):
		return http.StatusPreconditionFailed
	// A statement that outlived the table resolution it planned against --
	// the table truncated, or its definition changed, underneath it
	// (go-qrdb#547): 409, a conflict with the table's current state that
	// re-issuing the request resolves.
	case service.IsTableResolutionStale(err):
		return http.StatusConflict
	// A closed store is the node going away underneath the request, not a
	// broken server: 503 says "try another node or try again", which is true,
	// where 500 says "this request is poison", which is not (go-qrdb#371).
	//
	// It sits at the TOP of the table because the condition arrives wrapped —
	// in a BatchError, a ReadError, a ProposeError, a TimeSeriesError — and
	// every one of those wrappers has an errors.As arm further down that would
	// claim it first and answer 500. That is how the first cut of this rule
	// reached only the write path.
	//
	// It is matched with errors.Is, not errors.As: every producer unwraps to
	// the one sentinel, which is the whole point of the shared identity.
	case errors.Is(err, storage.ErrStorageClosed):
		return http.StatusServiceUnavailable
	// A system-scope encryption migration is rebuilding the data plane; the
	// service is back once it finishes, so retryable like a closed store.
	case errors.As(err, new(*service.DataPlaneRecyclingError)):
		return http.StatusServiceUnavailable
	// A request the protocol cannot encode was never proposed, so it is a
	// client error and not an infrastructure one. Reported as 500 it fires
	// an operational alert and invites a retry of an input that can never
	// succeed (go-qrdb#52).
	case errors.As(err, &ere):
		return http.StatusBadRequest
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
	case errors.As(err, &iite):
		return http.StatusRequestEntityTooLarge
	case errors.As(err, &iaete):
		return http.StatusRequestEntityTooLarge
	case errors.As(err, &itme):
		return http.StatusBadRequest
	case errors.As(err, &itmr):
		return http.StatusBadRequest
	case errors.As(err, &ipte):
		return http.StatusRequestEntityTooLarge
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
	// 409, like the collection-already-exists case above and unlike the 400s
	// around it: the request is well-formed and the caller may retry it
	// against a different id or delete the document first.
	case errors.As(err, &cde):
		return http.StatusConflict
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
	case errors.Is(err, tenant.ErrTenantSealed), errors.Is(err, seal.ErrSealed):
		// A barrier-isolated tenant's IAM (user/role) store resolved to a
		// SEALED barrier. The StoreResolver fails closed rather than leaking
		// the operation through the shared system store — surface 503 so the
		// caller retries after the tenant's barrier is unsealed, matching the
		// BarrierSealedError data-plane mapping. tenant.ErrTenantSealed is
		// what Resolve itself returns when the barrier is ALREADY sealed;
		// seal.ErrSealed is what the resolved store's own Get/Put return when
		// the barrier seals AFTER Resolve succeeded but before the store
		// operation runs — a live check on every call, not a cached
		// decision (go-qrdb#464 round 6) — and reaches here unwrapped from
		// tenantSharedKVStore's Encrypt/Decrypt.
		return http.StatusServiceUnavailable
	case errors.As(err, &ibu):
		// Required isolation backend not wired — fail closed as 503 rather than
		// silently serving the tenant from the shared store.
		return http.StatusServiceUnavailable
	case errors.As(err, &qee):
		// Numeric quota exceeded — surface as 429 Too Many Requests.
		return http.StatusTooManyRequests
	case errors.As(err, &qent):
		// Feature entitlement denied — surface as 403 Forbidden.
		return http.StatusForbidden
	case errors.As(err, &rtl), errors.As(err, &ptl), errors.As(err, &ktl):
		// The body exceeded the endpoint's byte bound. 413, not 400: the
		// request was well-formed enough to read, and the caller can act on it
		// by sending less.
		return http.StatusRequestEntityTooLarge
	case errors.As(err, &smnc), errors.As(err, &cue):
		// SM subsystem not configured, or a capability this state machine was
		// not built with — surface as 501 Not Implemented so clients know the
		// operation is not supported by this server instance.
		//
		// Both types are listed because go-qrdb#141 split them: capability
		// refusals moved to CapabilityUnsupportedError so gRPC could answer
		// Unimplemented instead of Internal. Mapping only the old type here
		// would have quietly demoted every REST capability refusal from 501 to
		// 500 — reintroducing on REST exactly the healthy-node-as-server-fault
		// behaviour that split was made to remove.
		return http.StatusNotImplemented
	case errors.As(err, &sqleq), errors.As(err, &sqled), errors.As(err, &sqlex):
		// SQL validation and engine failures are client errors — the
		// query (or its target database selector) is malformed.
		return http.StatusBadRequest
	case errors.As(err, &itce):
		// Forbidden tenant-scope character (e.g. '/' or '\') in the
		// x-qrdb-tenant header is a malformed request, not a server fault.
		return http.StatusBadRequest
	case errors.As(err, &irt):
		// Internally incoherent raft topology request (e.g. cluster mode
		// "none" on a dedicated-raft backend) is a malformed request.
		return http.StatusBadRequest
	case errors.As(err, &rdu):
		// Distributed raft topology requested but the placement subsystem is
		// not wired on this node — fail closed as 503 rather than silently
		// collapsing to a single local replica.
		return http.StatusServiceUnavailable
	case errors.As(err, &tqee):
		// Tenant-wide replica-usage reservation lost a concurrency race —
		// surface as 429 Too Many Requests, same as quota.ExceededError.
		return http.StatusTooManyRequests
	case errors.As(err, &infe):
		// Tenant is within its own quota but the cluster has no free node —
		// a distinct failure from quota-exceeded; 503 so a retry after
		// cluster capacity changes can succeed (unlike a 429 retry against
		// the same tenant quota, which never would).
		return http.StatusServiceUnavailable
	case errors.As(err, &rfe):
		// Requested replica count is out of range (< 1) — malformed request.
		return http.StatusBadRequest
	case errors.As(err, &ndre):
		// Target database has no dedicated-raft placement to scale.
		return http.StatusBadRequest
	case errors.As(err, &slte):
		// The dedicated shard's leader was not stable within the wait bound
		// (e.g. an in-flight election) — transient; 503 so the caller retries
		// once the group settles rather than seeing an opaque 500.
		return http.StatusServiceUnavailable
	case errors.As(err, &utoe):
		// A transaction branch (success/failure ops) contained a read
		// (Get/Range) or other unsupported op type — malformed request.
		return http.StatusBadRequest
	case errors.As(err, &cso):
		// A non-system tenant attempted a store-global MVCC compaction — the
		// service layer fails closed. 403, matching the ScopeCross authz gate.
		return http.StatusForbidden
	case errors.As(err, &tade):
		// A caller targeted a per-database control operation (seal/unseal/
		// barrier-status, drop) on a database owned by a different tenant. The
		// service layer fails closed independently of transport RBAC. 403.
		return http.StatusForbidden
	case errors.Is(err, watch.ErrCompacted):
		// A watch requested a start revision at or below the compacted floor
		// — the intervening history is gone. Same status family as
		// service.RevisionCompactedError.
		return http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, watch.ErrStartRevisionUnsupported):
		// A watch requested a start revision against a store with no change
		// history attached (a non-MVCC store) — replay is impossible.
		return http.StatusNotImplemented
	case errors.As(err, &mbr):
		// The shard's state machine keeps no MVCC revision history (the
		// default raft.sm_type is "pebble", not "mvcc") — 501 so the caller
		// learns the operation is unavailable on this deployment, not merely
		// that this one call failed.
		return http.StatusNotImplemented
	case errors.As(err, &rike):
		// A restore scope reached the internal key namespace — refused as an
		// authority the caller does not have, not a malformed request.
		return http.StatusForbidden
	case errors.As(err, &rciu):
		// A prefix-scoped restore needs the durable revision index and this
		// backend maintains none — same family as MVCCBackendRequiredError.
		return http.StatusNotImplemented
	case errors.As(err, &rnre), errors.As(err, &rrie), errors.As(err, &rrfe),
		errors.As(err, &rsee), errors.As(err, &rsae), errors.As(err, &reke),
		errors.As(err, &rste), errors.As(err, &rupe):
		// Request-shape guards: nil/malformed request, invalid or future
		// revision, ambiguous/empty/too-large/unbounded scope.
		return http.StatusBadRequest
	case errors.As(err, &rae):
		// A write in an already-validated plan failed part way through.
		return http.StatusInternalServerError
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
