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
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

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

// drainResponse is the JSON payload returned by POST /drain.
type drainResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// kvListResponse is the JSON payload returned by GET /kv?prefix=X.
type kvListResponse struct {
	Keys []string `json:"keys"`
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
// local node is not the leader and leader forwarding is configured, the
// request is transparently proxied to the leader.
func (s *Server) handleKVPut(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx := r.Context()
	if err := s.kvSvc.Put(ctx, key, body); err != nil {
		if s.tryForwardWrite(w, r, key, body, err) {
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &ProposeError{Err: err})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleKVGet handles GET /kv/{key} -- retrieves a value by key.
func (s *Server) handleKVGet(w http.ResponseWriter, r *http.Request, key string) {
	ctx := r.Context()
	data, err := s.kvSvc.Get(ctx, key)
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
	if err := s.kvSvc.Delete(ctx, key); err != nil {
		if s.tryForwardWrite(w, r, key, nil, err) {
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, &ProposeError{Err: err})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleKVList handles GET /kv?prefix=X -- lists keys by prefix.
func (s *Server) handleKVList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	ctx := r.Context()
	keys, err := s.kvSvc.List(ctx, prefix)
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, &ReadError{Err: err})
		return
	}

	writeJSON(w, kvListResponse{Keys: keys})
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
// fetchJSON
// ---------------------------------------------------------------------------

// fetchJSON issues an HTTP GET to the given URL and decodes the JSON
// response into dst. The caCertPool is used to verify the server's TLS
// certificate; when nil the system pool is used.
func fetchJSON(url string, dst any, caCertPool *x509.CertPool) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    caCertPool,
				MinVersion: tls.VersionTLS13,
			},
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
