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

// Package embedded implements the in-process transport for the QRDB SDK.
// It wraps the service.KVServicer and service.AdminServicer interfaces
// directly, bypassing network serialization for maximum performance when
// the QRDB engine is embedded in the same process.
package embedded

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport   = (*Transport)(nil)
	_ transport.KVClient    = (*Transport)(nil)
	_ transport.AdminClient = (*Transport)(nil)
	_ transport.Client      = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface by delegating
// directly to the service layer. No network I/O or serialization is
// performed, making this the fastest possible transport for embedded
// usage.
type Transport struct {
	kvSvc    service.KVServicer
	adminSvc service.AdminServicer
	closed   atomic.Bool
}

// New creates a new embedded transport that delegates to the given
// service implementations. Both services must be non-nil.
func New(kvSvc service.KVServicer, adminSvc service.AdminServicer) (*Transport, error) {
	if kvSvc == nil {
		return nil, &NilServiceError{Service: "KVServicer"}
	}
	if adminSvc == nil {
		return nil, &NilServiceError{Service: "AdminServicer"}
	}
	return &Transport{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
	}, nil
}

// Connect is a no-op for embedded transports since the service layer
// is already available in-process.
func (t *Transport) Connect(_ context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return nil
}

// Close marks the transport as closed. Subsequent operations will
// return ClosedError.
func (t *Transport) Close() error {
	t.closed.Store(true)
	return nil
}

// Healthy returns true when the transport is open. For embedded
// transports, the service layer is always reachable if not closed.
func (t *Transport) Healthy(_ context.Context) bool {
	return !t.closed.Load()
}

// Request dispatches a generic request to the appropriate service method
// based on the method name. This is provided for interface compliance;
// callers should prefer the typed KVClient and AdminClient methods.
func (t *Transport) Request(ctx context.Context, method string, req, resp any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	handler, ok := requestHandlers[method]
	if !ok {
		return &UnknownMethodError{Method: method}
	}

	return handler(ctx, t, req, resp)
}

// requestHandler is a function that handles a generic Request dispatch.
type requestHandler func(ctx context.Context, t *Transport, req, resp any) error

// requestHandlers maps method names to their handler functions for O(1)
// dispatch in the generic Request method.
var requestHandlers = map[string]requestHandler{
	"Put":             handlePut,
	"Get":             handleGet,
	"Delete":          handleDelete,
	"List":            handleList,
	"Health":          handleHealth,
	"Status":          handleStatus,
	"Peers":           handlePeers,
	"AddMember":       handleAddMember,
	"RemoveMember":    handleRemoveMember,
	"RequestSnapshot": handleRequestSnapshot,
	"Drain":           handleDrain,
}

// ---------------------------------------------------------------------------
// KVClient implementation
// ---------------------------------------------------------------------------

// Put stores a key-value pair through the embedded KV service.
func (t *Transport) Put(ctx context.Context, key string, value []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.kvSvc.Put(ctx, key, value))
}

// Get retrieves a value by key through the embedded KV service.
func (t *Transport) Get(ctx context.Context, key string) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	data, err := t.kvSvc.Get(ctx, key)
	if err != nil {
		return nil, translateError(err)
	}
	return data, nil
}

// Delete removes a key through the embedded KV service.
func (t *Transport) Delete(ctx context.Context, key string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.kvSvc.Delete(ctx, key))
}

// List returns sorted keys matching the given prefix through the
// embedded KV service.
func (t *Transport) List(ctx context.Context, prefix string) ([]string, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	keys, err := t.kvSvc.List(ctx, prefix)
	if err != nil {
		return nil, translateError(err)
	}
	return keys, nil
}

// ---------------------------------------------------------------------------
// AdminClient implementation
// ---------------------------------------------------------------------------

// Health returns the health status through the embedded admin service.
func (t *Transport) Health(ctx context.Context) (*transport.HealthResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	status, err := t.adminSvc.Health(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.HealthResponse{
		Status:   status.Status,
		IsLeader: status.Leader,
	}, nil
}

// Status returns detailed node status through the embedded admin service.
func (t *Transport) Status(ctx context.Context) (*transport.StatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	info, err := t.adminSvc.Status(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.StatusResponse{
		NodeID:      info.NodeID,
		RaftAddress: info.RaftAddress,
		IsLeader:    info.IsLeader,
		LeaderID:    info.LeaderID,
		ShardCount:  info.ShardCount,
		IsDraining:  info.IsDraining,
		Shards:      info.Shards,
	}, nil
}

// Peers returns the peer membership through the embedded admin service.
func (t *Transport) Peers(ctx context.Context) (*transport.PeersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	info, err := t.adminSvc.Peers(ctx)
	if err != nil {
		return nil, translateError(err)
	}

	shards := make([]transport.ShardPeers, 0, len(info.Shards))
	for _, sp := range info.Shards {
		peers := make([]transport.PeerInfo, 0, len(sp.Peers))
		for _, p := range sp.Peers {
			peers = append(peers, transport.PeerInfo{
				ReplicaID: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			})
		}
		shards = append(shards, transport.ShardPeers{
			ShardID:  sp.ShardID,
			LeaderID: sp.LeaderID,
			Peers:    peers,
		})
	}

	return &transport.PeersResponse{Shards: shards}, nil
}

// AddMember adds a new replica through the embedded admin service.
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.AddMember(ctx, shardID, replicaID, raftAddr))
}

// RemoveMember removes a replica through the embedded admin service.
func (t *Transport) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.RemoveMember(ctx, shardID, replicaID))
}

// RequestSnapshot triggers a snapshot through the embedded admin service.
func (t *Transport) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.RequestSnapshot(ctx, shardID))
}

// Drain transitions the host into drain mode through the embedded
// admin service.
func (t *Transport) Drain(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.Drain(ctx))
}

// ---------------------------------------------------------------------------
// Generic Request dispatch handlers
// ---------------------------------------------------------------------------

func handlePut(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*putRequest)
	if !ok {
		return &InvalidRequestError{Method: "Put"}
	}
	return t.Put(ctx, r.Key, r.Value)
}

func handleGet(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*getRequest)
	if !ok {
		return &InvalidRequestError{Method: "Get"}
	}
	data, err := t.Get(ctx, r.Key)
	if err != nil {
		return err
	}
	if out, ok := resp.(*[]byte); ok {
		*out = data
	}
	return nil
}

func handleDelete(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*deleteRequest)
	if !ok {
		return &InvalidRequestError{Method: "Delete"}
	}
	return t.Delete(ctx, r.Key)
}

func handleList(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*listRequest)
	if !ok {
		return &InvalidRequestError{Method: "List"}
	}
	keys, err := t.List(ctx, r.Prefix)
	if err != nil {
		return err
	}
	if out, ok := resp.(*[]string); ok {
		*out = keys
	}
	return nil
}

func handleHealth(ctx context.Context, t *Transport, _, resp any) error {
	health, err := t.Health(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.HealthResponse); ok {
		*out = health
	}
	return nil
}

func handleStatus(ctx context.Context, t *Transport, _, resp any) error {
	status, err := t.Status(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.StatusResponse); ok {
		*out = status
	}
	return nil
}

func handlePeers(ctx context.Context, t *Transport, _, resp any) error {
	peers, err := t.Peers(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.PeersResponse); ok {
		*out = peers
	}
	return nil
}

func handleAddMember(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.AddMemberRequest)
	if !ok {
		return &InvalidRequestError{Method: "AddMember"}
	}
	return t.AddMember(ctx, r.ShardID, r.ReplicaID, r.Address)
}

func handleRemoveMember(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.RemoveMemberRequest)
	if !ok {
		return &InvalidRequestError{Method: "RemoveMember"}
	}
	return t.RemoveMember(ctx, r.ShardID, r.ReplicaID)
}

func handleRequestSnapshot(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.SnapshotRequest)
	if !ok {
		return &InvalidRequestError{Method: "RequestSnapshot"}
	}
	return t.RequestSnapshot(ctx, r.ShardID)
}

func handleDrain(ctx context.Context, t *Transport, _, _ any) error {
	return t.Drain(ctx)
}

// ---------------------------------------------------------------------------
// Generic request types for Request dispatch
// ---------------------------------------------------------------------------

type putRequest struct {
	Key   string
	Value []byte
}

type getRequest struct {
	Key string
}

type deleteRequest struct {
	Key string
}

type listRequest struct {
	Prefix string
}
