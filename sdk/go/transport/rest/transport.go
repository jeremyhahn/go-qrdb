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

// Package rest implements the REST (HTTP/2) transport for the QRDB SDK.
// It communicates with the QRDB REST API server using HTTP/2 over TLS
// or plain HTTP for development.
package rest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport        = (*Transport)(nil)
	_ transport.KVClient         = (*Transport)(nil)
	_ transport.AdminClient      = (*Transport)(nil)
	_ transport.TimeSeriesClient = (*Transport)(nil)
	_ transport.DatabaseScoper   = (*Transport)(nil)
)

// dbHeaderRoundTripper injects the X-QRDB-Database header on data-plane
// requests before delegating to the underlying RoundTripper. It is a no-op
// when database is empty or the request path is not a data-plane endpoint.
type dbHeaderRoundTripper struct {
	base     http.RoundTripper
	database string
}

// RoundTrip implements http.RoundTripper.
func (rt *dbHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.database != "" && transport.IsDataPlanePath(req.URL.Path) {
		// Clone before mutating headers per the RoundTripper contract.
		clone := req.Clone(req.Context())
		clone.Header.Set(transport.DatabaseHeader, rt.database)
		return rt.base.RoundTrip(clone)
	}
	return rt.base.RoundTrip(req)
}

// Transport implements the QRDB SDK transport interface using HTTP/2 REST
// requests. It communicates with the QRDB REST API server, which uses the
// following routes:
//
//	KV:    PUT /kv/{key}, GET /kv/{key}, DELETE /kv/{key}, GET /kv?prefix=X
//	Admin: GET /health, GET /status, GET /peers
//	       POST /membership/add, POST /membership/remove, POST /snapshot, POST /drain
//	       POST /admin/shards, DELETE /admin/shards/{id}, GET /admin/shards/{id}/state
//	       GET /admin/topology
type Transport struct {
	config  *transport.Config
	client  *http.Client
	baseURL string
	closed  atomic.Bool

	// activeDatabase, when non-empty, is sent as the X-QRDB-Database header on
	// every data-plane request so the server namespaces the operation under
	// {tenant}/{database}/. Empty means unscoped (server uses the system
	// database). It is set only via WithDatabase and is immutable per Transport.
	activeDatabase string

	// baseRT is the underlying RoundTripper (HTTP/2 transport) shared across
	// database-scoped clones so they reuse the same connection pool.
	baseRT http.RoundTripper

	// httpTransport is the connection pool ensureClient built, kept so a call
	// that must stay on one connection (tenant barrier init's proof of
	// possession) can clone its TLS configuration into a pinned client.
	httpTransport *http.Transport
}

// New creates a new REST transport with the given options. The transport
// is not connected until Connect is called.
func New(opts ...transport.Option) (*Transport, error) {
	cfg := transport.ApplyOptions(opts...)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	t := &Transport{
		config:  cfg,
		baseURL: strings.TrimRight(cfg.Address, "/"),
	}

	return t, nil
}

// ensureClient builds the HTTP client and TLS configuration if one is not
// already in place. Connect calls it before probing reachability;
// RedeemInvitation calls it directly, with no reachability probe, since it
// is the one call an unconnected client may make (see
// transport.InvitationClient) and a sealed or not-yet-reachable server still
// answers it the same opaque way as a bad token.
func (t *Transport) ensureClient() error {
	if t.client != nil {
		return nil
	}

	httpTransport := &http.Transport{
		ForceAttemptHTTP2: true,
		MaxIdleConns:      100,
		IdleConnTimeout:   90 * time.Second,
	}

	tlsCfg, err := t.buildTLSConfig()
	if err != nil {
		return err
	}
	if tlsCfg != nil {
		httpTransport.TLSClientConfig = tlsCfg
	}

	timeout := t.config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Every request made with a context bound by transport.WithSOPIN carries
	// the SO PIN header -- over https to the configured host only, on every
	// hop including redirects.
	t.httpTransport = httpTransport
	t.baseRT = transport.SOPINRoundTripper(httpTransport, "rest", transport.ConfiguredHost(t.baseURL))
	t.client = &http.Client{
		Transport: t.scopedRoundTripper(),
		Timeout:   timeout,
	}
	return nil
}

// Connect establishes the HTTP client with TLS configuration. For REST
// transports, this initializes the HTTP client and verifies connectivity
// with a health check.
func (t *Transport) Connect(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if err := t.ensureClient(); err != nil {
		return err
	}

	// Verify connectivity with a reachability probe. We accept the server as
	// connectable when /health returns 200 (healthy) OR 503 (sealed /
	// not-ready). A sealed node refuses /health with 503, yet it is up and
	// serves the unseal call, which an operator MUST be able to reach over
	// REST — gating Connect on 200 alone would make the CLI unable to unseal
	// a node at all. Other statuses (e.g. 500) and transport errors are
	// treated as unreachable.
	if !t.reachable(ctx) {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &healthCheckError{},
		}
	}

	return nil
}

// reachable reports whether the server is connectable: /health answers with
// 200 (healthy) or 503 (sealed / not-ready). It is intentionally more
// permissive than Healthy so the client can attach to a sealed server and
// run the allowlisted barrier/seal commands. Transport errors and other
// status codes return false.
func (t *Transport) reachable(ctx context.Context) bool {
	if t.closed.Load() || t.client == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusServiceUnavailable
}

// Close shuts down the REST transport and releases idle connections.
func (t *Transport) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	if t.client != nil {
		t.client.CloseIdleConnections()
	}
	return nil
}

// Healthy returns true if the QRDB server responds to a health check.
func (t *Transport) Healthy(ctx context.Context) bool {
	if t.closed.Load() || t.client == nil {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/health", nil)
	if err != nil {
		return false
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	return resp.StatusCode == http.StatusOK
}

// Request performs a generic request dispatch. This is primarily used for
// extensibility; callers should prefer the typed KVClient and AdminClient
// methods.
func (t *Transport) Request(ctx context.Context, method string, req, resp any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	var body io.Reader
	if req != nil {
		data, err := json.Marshal(req)
		if err != nil {
			return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
		}
		body = bytes.NewReader(data)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/"+method, body)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, httpResp)
	}

	if resp != nil {
		if err := json.NewDecoder(httpResp.Body).Decode(resp); err != nil {
			return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// KVClient implementation
// ---------------------------------------------------------------------------

// Put stores a key-value pair. Maps to PUT /kv/{key} with the raw value
// as the request body. An optional ?write_mode= query parameter is appended
// when a write mode was explicitly set.
func (t *Transport) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(value))
	if err != nil {
		return &transport.RequestError{Method: "Put", Err: err}
	}

	applyWriteModeQueryParam(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return &transport.RequestError{Method: "Put", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	return t.decodeErrorResponse("Put", resp)
}

// Get retrieves a value by key. Maps to GET /kv/{key}. Returns
// KeyNotFoundError when the server responds with 404. An optional
// ?consistency= query parameter is appended when a consistency level was set.
func (t *Transport) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Get", Err: err}
	}

	applyReadQueryParams(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Get", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.KeyNotFoundError{Key: key}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("Get", resp)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &transport.RequestError{Method: "Get", Err: &readBodyError{Err: err}}
	}

	return data, nil
}

// Delete removes a key. Maps to DELETE /kv/{key}. An optional ?write_mode=
// query parameter is appended when a write mode was explicitly set.
func (t *Transport) Delete(ctx context.Context, key string, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "Delete", Err: err}
	}

	applyWriteModeQueryParam(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return &transport.RequestError{Method: "Delete", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	return t.decodeErrorResponse("Delete", resp)
}

// List returns sorted keys matching the given prefix. Maps to GET /kv?prefix=X.
// An optional ?consistency= query parameter is appended when a consistency
// level was set.
func (t *Transport) List(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if prefix != "" {
		params.Set("prefix", prefix)
	}
	reqURL := t.baseURL + "/kv"
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "List", Err: err}
	}

	applyReadQueryParams(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "List", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("List", resp)
	}

	var listResp transport.KVListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "List", Err: &jsonDecodeError{Err: err}}
	}

	return listResp.Keys, nil
}

// Scan returns all key-value pairs matching the given prefix. Maps to
// GET /kv/scan?prefix=X. An optional ?consistency= query parameter is appended
// when a consistency level was set.
func (t *Transport) Scan(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if prefix != "" {
		params.Set("prefix", prefix)
	}
	reqURL := t.baseURL + "/kv/scan"
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Scan", Err: err}
	}

	applyReadQueryParams(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Scan", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("Scan", resp)
	}

	var scanResp transport.KVScanResponse
	if decErr := json.NewDecoder(resp.Body).Decode(&scanResp); decErr != nil {
		return nil, &transport.RequestError{Method: "Scan", Err: &jsonDecodeError{Err: decErr}}
	}

	return scanResp.Pairs, nil
}

// Exists checks whether a key exists. Performs a HEAD-like check via Get.
func (t *Transport) Exists(ctx context.Context, key string, opts ...transport.ReadOption) (bool, error) {
	_, err := t.Get(ctx, key, opts...)
	if err != nil {
		var knf *transport.KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// AdminClient implementation
// ---------------------------------------------------------------------------

// Health returns the health status of the QRDB node. Maps to GET /health.
func (t *Transport) Health(ctx context.Context) (*transport.HealthResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/health", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Health", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Health", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	var healthResp transport.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		return nil, &transport.RequestError{Method: "Health", Err: &jsonDecodeError{Err: err}}
	}

	return &healthResp, nil
}

// Status returns detailed node and shard status. Maps to GET /status.
func (t *Transport) Status(ctx context.Context) (*transport.StatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Status", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Status", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("Status", resp)
	}

	var statusResp transport.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, &transport.RequestError{Method: "Status", Err: &jsonDecodeError{Err: err}}
	}

	return &statusResp, nil
}

// Peers returns the peer membership for all loaded shards. Maps to
// GET /peers.
func (t *Transport) Peers(ctx context.Context) (*transport.PeersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/peers", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Peers", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Peers", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("Peers", resp)
	}

	var peersResp transport.PeersResponse
	if err := json.NewDecoder(resp.Body).Decode(&peersResp); err != nil {
		return nil, &transport.RequestError{Method: "Peers", Err: &jsonDecodeError{Err: err}}
	}

	return &peersResp, nil
}

// AddMember adds a new replica to the specified shard. Maps to
// POST /membership/add.
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.AddMemberRequest{
		ShardID:   shardID,
		ReplicaID: replicaID,
		Address:   raftAddr,
		Identity:  identity,
	}

	return t.postJSON(ctx, "AddMember", "/membership/add", payload)
}

// RemoveMember removes a replica from the specified shard. Maps to
// POST /membership/remove.
func (t *Transport) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.RemoveMemberRequest{
		ShardID:   shardID,
		ReplicaID: replicaID,
	}

	return t.postJSON(ctx, "RemoveMember", "/membership/remove", payload)
}

// PromoteMember promotes a committed observer of the specified shard to a
// voter. Maps to POST /membership/promote. Implements
// transport.MemberPromoter.
func (t *Transport) PromoteMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.PromoteMemberRequest{
		ShardID:   shardID,
		ReplicaID: replicaID,
	}

	return t.postJSON(ctx, "PromoteMember", "/membership/promote", payload)
}

// RetireJoinIdentity permanently refuses any future admission for a node
// identity. Maps to POST /v1/cluster/join-identities/retire.
func (t *Transport) RetireJoinIdentity(ctx context.Context, nodeID, reason string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.RetireJoinIdentityRequest{
		NodeID: nodeID,
		Reason: reason,
	}

	return t.postJSON(ctx, "RetireJoinIdentity", "/v1/cluster/join-identities/retire", payload)
}

// RequestSnapshot triggers an asynchronous snapshot for the specified
// shard. Maps to POST /snapshot.
func (t *Transport) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.SnapshotRequest{
		ShardID: shardID,
	}

	return t.postJSON(ctx, "RequestSnapshot", "/snapshot", payload)
}

// MintJoinToken mints a single-use, identity-bound member-side CSR join
// token. Maps to POST /v1/cluster/join-tokens.
var _ transport.ClusterJoinTokenMinter = (*Transport)(nil)

func (t *Transport) MintJoinToken(ctx context.Context, req *transport.MintJoinTokenRequest) (*transport.MintJoinTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.MintJoinTokenResponse
	if err := t.postJSONDecode(ctx, "MintJoinToken", "/v1/cluster/join-tokens", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AddObserverMember adds a new replica to the specified shard as a
// non-voting observer rather than a voter. Maps to
// POST /membership/add-observer. Implements transport.ObserverMemberAdder.
func (t *Transport) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.AddObserverMemberRequest{
		ShardID:   shardID,
		ReplicaID: replicaID,
		Address:   raftAddr,
		Identity:  identity,
	}

	return t.postJSON(ctx, "AddObserverMember", "/membership/add-observer", payload)
}

var _ transport.ObserverMemberAdder = (*Transport)(nil)

// TransferLeadership hands shardID's leadership to replicaID and returns once
// it leads. Maps to POST /membership/transfer-leadership. Implements
// transport.LeaderTransferrer.
func (t *Transport) TransferLeadership(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.TransferLeadershipRequest{
		ShardID:   shardID,
		ReplicaID: replicaID,
	}

	return t.postJSON(ctx, "TransferLeadership", "/membership/transfer-leadership", payload)
}

var _ transport.LeaderTransferrer = (*Transport)(nil)

// restPendingJoinRequest is the wire shape GET /v1/cluster/join-requests
// returns one entry as.
type restPendingJoinRequest struct {
	ID          string    `json:"id"`
	NodeURI     string    `json:"node_uri,omitempty"`
	ReplicaID   uint64    `json:"replica_id,omitempty"`
	Features    []string  `json:"features,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// restListPendingJoinRequestsResponse is the wire shape of GET
// /v1/cluster/join-requests.
type restListPendingJoinRequestsResponse struct {
	Requests []restPendingJoinRequest `json:"requests"`
}

// ListPendingJoinRequests lists tokenless member-side CSR join requests
// awaiting an operator's decision. Maps to GET /v1/cluster/join-requests.
// Implements transport.JoinRequestReviewer.
func (t *Transport) ListPendingJoinRequests(ctx context.Context) ([]*transport.PendingJoinRequest, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp restListPendingJoinRequestsResponse
	if err := t.getJSONDecode(ctx, "ListPendingJoinRequests", "/v1/cluster/join-requests", &resp); err != nil {
		return nil, err
	}

	out := make([]*transport.PendingJoinRequest, 0, len(resp.Requests))
	for _, r := range resp.Requests {
		out = append(out, &transport.PendingJoinRequest{
			ID:          r.ID,
			NodeURI:     r.NodeURI,
			ReplicaID:   r.ReplicaID,
			Features:    r.Features,
			SubmittedAt: r.SubmittedAt,
			ExpiresAt:   r.ExpiresAt,
		})
	}
	return out, nil
}

// ApproveJoinRequest binds id's queued CSR to an operator-chosen node
// identity. Maps to POST /v1/cluster/join-requests/approve. Implements
// transport.JoinRequestReviewer.
func (t *Transport) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.ApproveJoinRequestRequest{ID: id, NodeID: nodeID}
	return t.postJSON(ctx, "ApproveJoinRequest", "/v1/cluster/join-requests/approve", payload)
}

// RejectJoinRequest records a terminal refusal of id's queued CSR. Maps to
// POST /v1/cluster/join-requests/reject. Implements
// transport.JoinRequestReviewer.
func (t *Transport) RejectJoinRequest(ctx context.Context, id, reason string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.RejectJoinRequestRequest{ID: id, Reason: reason}
	return t.postJSON(ctx, "RejectJoinRequest", "/v1/cluster/join-requests/reject", payload)
}

var _ transport.JoinRequestReviewer = (*Transport)(nil)

// Capabilities returns the compile-time feature set of the QRDB node.
// Maps to GET /capabilities.
func (t *Transport) Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/capabilities", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Capabilities", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Capabilities", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("Capabilities", resp)
	}

	var capsResp transport.CapabilitiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&capsResp); err != nil {
		return nil, &transport.RequestError{Method: "Capabilities", Err: &jsonDecodeError{Err: err}}
	}

	return &capsResp, nil
}

// Drain transitions the host into drain mode. Maps to POST /drain.
func (t *Transport) Drain(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/drain", nil)
	if err != nil {
		return &transport.RequestError{Method: "Drain", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &transport.RequestError{Method: "Drain", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	var drainResp transport.DrainResponse
	if err := json.NewDecoder(resp.Body).Decode(&drainResp); err != nil {
		return &transport.RequestError{Method: "Drain", Err: &jsonDecodeError{Err: err}}
	}

	if resp.StatusCode != http.StatusOK || drainResp.Status != "drained" {
		return &transport.RequestError{
			Method:     "Drain",
			StatusCode: resp.StatusCode,
			Err:        &drainFailedError{Status: drainResp.Status, Detail: drainResp.Error},
		}
	}

	return nil
}

// StartShard initializes and starts a Raft group for the given shard.
// Maps to POST /admin/shards with a JSON request body.
func (t *Transport) StartShard(ctx context.Context, req *transport.StartShardRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSON(ctx, "StartShard", "/admin/shards", req)
}

// StopShard stops the Raft group for the given shard. Maps to
// DELETE /admin/shards/{shardID}.
func (t *Transport) StopShard(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/admin/shards/" + strconv.FormatUint(shardID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "StopShard", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &transport.RequestError{Method: "StopShard", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	return t.decodeErrorResponse("StopShard", resp)
}

// GetShardState returns the Raft state for a shard. Maps to
// GET /admin/shards/{shardID}/state.
func (t *Transport) GetShardState(ctx context.Context, shardID uint64) (*transport.ShardStateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/admin/shards/" + strconv.FormatUint(shardID, 10) + "/state"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetShardState", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetShardState", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetShardState", resp)
	}

	var stateResp transport.ShardStateResponse
	if err := json.NewDecoder(resp.Body).Decode(&stateResp); err != nil {
		return nil, &transport.RequestError{Method: "GetShardState", Err: &jsonDecodeError{Err: err}}
	}

	return &stateResp, nil
}

// GetTopology returns the full cluster topology. Maps to
// GET /admin/topology.
func (t *Transport) GetTopology(ctx context.Context) (*transport.TopologyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/admin/topology", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTopology", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTopology", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetTopology", resp)
	}

	var topoResp transport.TopologyResponse
	if err := json.NewDecoder(resp.Body).Decode(&topoResp); err != nil {
		return nil, &transport.RequestError{Method: "GetTopology", Err: &jsonDecodeError{Err: err}}
	}

	return &topoResp, nil
}

// ReplicaStates implements transport.AdminClient.
func (t *Transport) ReplicaStates(ctx context.Context, shardID uint64) (*transport.ReplicaStatesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/admin/replica-states"
	if shardID != 0 {
		reqURL += "?shard_id=" + strconv.FormatUint(shardID, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReplicaStates", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReplicaStates", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ReplicaStates", resp)
	}

	var states transport.ReplicaStatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&states); err != nil {
		return nil, &transport.RequestError{Method: "ReplicaStates", Err: &jsonDecodeError{Err: err}}
	}
	if states.Replicas == nil {
		states.Replicas = []transport.ReplicaState{}
	}
	return &states, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// scopedRoundTripper wraps the base RoundTripper so that data-plane requests
// carry the active database header. When no database is scoped it returns the
// base transport unchanged.
func (t *Transport) scopedRoundTripper() http.RoundTripper {
	if t.activeDatabase == "" {
		return t.baseRT
	}
	return &dbHeaderRoundTripper{base: t.baseRT, database: t.activeDatabase}
}

// WithDatabase returns a database-scoped clone of the transport. The clone
// shares the same underlying HTTP connection pool and configuration but tags
// every data-plane request with the X-QRDB-Database header. Scoping to the
// default ("system") database, or the empty string, yields an unscoped clone.
func (t *Transport) withDatabase(database string) *Transport {
	db := transport.NormalizeDatabaseScope(database)
	clone := &Transport{
		config:         t.config,
		baseURL:        t.baseURL,
		baseRT:         t.baseRT,
		httpTransport:  t.httpTransport,
		activeDatabase: db,
	}
	clone.closed.Store(t.closed.Load())
	if t.client != nil {
		clone.client = &http.Client{
			Transport: clone.scopedRoundTripper(),
			Timeout:   t.client.Timeout,
		}
	}
	return clone
}

// WithDatabase implements transport.DatabaseScoper. Scoping a network
// transport never fails: the database travels with every data-plane request
// and the server resolves it.
func (t *Transport) WithDatabase(database string) (transport.Transport, error) {
	return t.withDatabase(database), nil
}

// buildTLSConfig constructs the TLS configuration from the transport config.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	return transport.BuildTLSConfig(transport.TLSBuildOptions{
		Custom:   t.config.TLSConfig,
		Enabled:  t.config.TLSEnabled,
		CAFile:   t.config.TLSCAFile,
		CertFile: t.config.TLSCertFile,
		KeyFile:  t.config.TLSKeyFile,
	})
}

// postJSON encodes a payload as JSON and POSTs it to the given path.
// It expects a 200 OK response with a JSON body containing {"status":"ok"}.
func (t *Transport) postJSON(ctx context.Context, method, path string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return t.decodeErrorResponse(method, resp)
	}

	return nil
}

// decodeErrorResponse reads the response body and constructs a
// RequestError. It attempts to decode the JSON error body; if that
// fails, the raw body text is used.
func (t *Transport) decodeErrorResponse(method string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var msg string
	var errResp transport.ErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		msg = errResp.Error
	} else if msg = strings.TrimSpace(string(body)); msg == "" {
		msg = resp.Status
	}
	// A 503 alone also means a node that is not ready; the SEALED code is what
	// makes it the sealed node's refusal.
	if resp.StatusCode == http.StatusServiceUnavailable && errResp.Code == sealedwindow.ReasonSealed {
		return &transport.BarrierSealedError{Message: msg}
	}

	err := transport.RefusalError(errResp.Code, errResp.Metadata)
	if err == nil {
		err = serverErrorFor(resp.StatusCode, msg)
	}
	return &transport.RequestError{
		Method:     method,
		StatusCode: resp.StatusCode,
		Err:        err,
		RetryAfter: retryAfterHeader(resp.Header.Get("Retry-After")),
		Reason:     errResp.Code,
		Metadata:   errResp.Metadata,
	}
}

// retryAfterHeader reads a Retry-After header (RFC 9110 section 10.2.3):
// delay-seconds, which is what the server sends, or an HTTP-date, read as
// the time remaining until it. A header that is absent, malformed or in the
// past names no delay.
func retryAfterHeader(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

// serverErrorFor types the server's refusal where its status is unambiguous.
//
// A 501 is the capability refusal and nothing else: since go-qrdb#557 the
// server classifies every error through pkg/api/errclass, and the only class it
// renders as 501 is Unimplemented — a state machine without the opcode or
// subsystem, a service this server was built without. The refusals decided on
// a request's merits, which used to share that status and forced the
// go-qrdb#126 exclusion, now answer 400 and 409. So every REST method hands a
// caller transport.CapabilityUnsupportedError for a 501, exactly as the
// embedded, gRPC, unix, QUIC and MCP transports do, and the RequestError
// around it keeps the method and status.
func serverErrorFor(status int, msg string) error {
	if status == http.StatusNotImplemented {
		return &transport.CapabilityUnsupportedError{Message: msg}
	}
	return &serverError{Message: msg}
}

// applyReadQueryParams resolves read options and appends the corresponding
// query parameters to req.URL. It is a no-op when no read options are set.
//
// Mapping:
//   - ConsistencySet  → ?consistency=<level>
//   - AllowStale only → ?consistency=eventual (legacy back-compat)
//
// The TimeoutMs field is not wired as a query parameter because the server
// enforces its own request timeout; it is kept for future extension.
func applyReadQueryParams(req *http.Request, opts ...transport.ReadOption) {
	ro := transport.ResolveReadOptions(opts...)
	if !ro.ConsistencySet && !ro.AllowStale {
		return
	}

	q := req.URL.Query()
	if ro.ConsistencySet {
		q.Set("consistency", ro.Consistency.String())
	} else if ro.AllowStale {
		q.Set("consistency", "eventual")
	}
	req.URL.RawQuery = q.Encode()
}

// applyWriteModeQueryParam appends the ?write_mode= query parameter to req.URL
// when a non-default write mode was explicitly set via WriteOption.
func applyWriteModeQueryParam(req *http.Request, opts ...transport.WriteOption) {
	wo := transport.ResolveWriteOptions(opts...)
	if !wo.ModeSet {
		return
	}
	q := req.URL.Query()
	q.Set("write_mode", wo.Mode.String())
	req.URL.RawQuery = q.Encode()
}
