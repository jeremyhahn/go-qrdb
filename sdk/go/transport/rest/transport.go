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
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport   = (*Transport)(nil)
	_ transport.KVClient    = (*Transport)(nil)
	_ transport.AdminClient = (*Transport)(nil)
	_ transport.Client      = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface using HTTP/2 REST
// requests. It communicates with the QRDB REST API server, which uses the
// following routes:
//
//	KV:    PUT /kv/{key}, GET /kv/{key}, DELETE /kv/{key}, GET /kv?prefix=X
//	Admin: GET /health, GET /status, GET /peers
//	       POST /membership/add, POST /membership/remove, POST /snapshot, POST /drain
type Transport struct {
	config  *transport.Config
	client  *http.Client
	baseURL string
	closed  atomic.Bool
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

// Connect establishes the HTTP client with TLS configuration. For REST
// transports, this initializes the HTTP client and verifies connectivity
// with a health check.
func (t *Transport) Connect(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
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

	t.client = &http.Client{
		Transport: httpTransport,
		Timeout:   timeout,
	}

	// Verify connectivity with a health check.
	if !t.Healthy(ctx) {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &healthCheckError{},
		}
	}

	return nil
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
// as the request body.
func (t *Transport) Put(ctx context.Context, key string, value []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(value))
	if err != nil {
		return &transport.RequestError{Method: "Put", Err: err}
	}

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
// KeyNotFoundError when the server responds with 404.
func (t *Transport) Get(ctx context.Context, key string) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Get", Err: err}
	}

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

// Delete removes a key. Maps to DELETE /kv/{key}.
func (t *Transport) Delete(ctx context.Context, key string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "Delete", Err: err}
	}

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

// List returns sorted keys matching the given prefix. Maps to
// GET /kv?prefix=X.
func (t *Transport) List(ctx context.Context, prefix string) ([]string, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv"
	if prefix != "" {
		reqURL += "?prefix=" + url.QueryEscape(prefix)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "List", Err: err}
	}

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
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.AddMemberRequest{
		ShardID:   shardID,
		ReplicaID: replicaID,
		Address:   raftAddr,
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

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildTLSConfig constructs the TLS configuration from the transport config.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	// Use custom TLS config if provided.
	if t.config.TLSConfig != nil {
		return t.config.TLSConfig, nil
	}

	if !t.config.TLSEnabled {
		return nil, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	// Load CA certificate pool.
	if t.config.TLSCAFile != "" {
		caCert, err := os.ReadFile(t.config.TLSCAFile)
		if err != nil {
			return nil, &transport.TLSError{Err: &caFileReadError{Path: t.config.TLSCAFile, Err: err}}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, &transport.TLSError{Err: &caParseError{Path: t.config.TLSCAFile}}
		}
		tlsCfg.RootCAs = pool
	}

	// Load client certificate for mTLS.
	if t.config.TLSCertFile != "" && t.config.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.config.TLSCertFile, t.config.TLSKeyFile)
		if err != nil {
			return nil, &transport.TLSError{Err: &certLoadError{Err: err}}
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
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

	var errResp transport.ErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return &transport.RequestError{
			Method:     method,
			StatusCode: resp.StatusCode,
			Err:        &serverError{Message: errResp.Error},
		}
	}

	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}

	return &transport.RequestError{
		Method:     method,
		StatusCode: resp.StatusCode,
		Err:        &serverError{Message: msg},
	}
}
