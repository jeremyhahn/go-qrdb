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

package service

import (
	"context"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ClusterJoinTokenMinter mints single-use, identity-bound member-side CSR
// join tokens (see pkg/clusterenroll.MintJoinToken) into the replicated
// join-token store. It is a distinct interface from AdminServicer, not a
// method added to it: minting a token for an unauthenticated joiner is an
// admission decision orthogonal to the authenticated cluster-membership
// verbs AdminServicer already carries, and every transport binding that
// wants to expose it type-asserts for this interface
// (adminSvc.(ClusterJoinTokenMinter)) rather than every AdminServicer
// implementation -- including every test double across this repo -- needing
// to grow a method most of them have no store to serve. AdminService
// implements it once configured (WithClusterJoinTokenStore or
// SetClusterJoinTokenStore).
//
// cluster:join_token is ScopeCross and platform-only (pkg/permission): only
// an identity of the system tenant may mint a token, on every transport that
// wires this interface.
type ClusterJoinTokenMinter interface {
	// MintJoinToken mints a token bound to spiffe://<trust domain>/node/
	// <nodeID> with the given time-to-live. See clusterenroll.MintJoinToken
	// for the single-use, CAS-consumed semantics.
	MintJoinToken(ctx context.Context, nodeID string, ttl time.Duration) (token string, expiresAt time.Time, err error)
}

// ClusterJoinTokenNotConfiguredError is returned by AdminService.
// MintJoinToken when no join-token store and trust domain have been
// configured (WithClusterJoinTokenStore / SetClusterJoinTokenStore). A node
// that never enabled member-side CSR join (--cluster-join-enabled) has
// nothing to mint against.
type ClusterJoinTokenNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *ClusterJoinTokenNotConfiguredError) Error() string {
	return "service: no cluster join-token store configured; enable member-side CSR join first"
}

// clusterJoinTokenState holds AdminService's join-token minting
// configuration behind a mutex: it is set once during startup (after the
// replicated KV store exists, which is later than AdminService's own
// construction -- see SetClusterJoinTokenStore) but MintJoinToken must be
// safe to call concurrently with that late bind.
type clusterJoinTokenState struct {
	mu          sync.RWMutex
	store       kvstore.AtomicKVStore
	trustDomain string
}

// WithClusterJoinTokenStore configures AdminService to mint member-side CSR
// join tokens into store under trustDomain, for composition roots that have
// both available at construction time (e.g. the SDK's embedded transport).
// Composition roots where the store is only available after the host has
// started (cmd/qrdbd) use SetClusterJoinTokenStore instead.
func WithClusterJoinTokenStore(store kvstore.AtomicKVStore, trustDomain string) AdminServiceOption {
	return func(s *AdminService) {
		s.SetClusterJoinTokenStore(store, trustDomain)
	}
}

// SetClusterJoinTokenStore configures (or reconfigures) this AdminService's
// join-token minting store and trust domain after construction. Safe to
// call concurrently with MintJoinToken.
func (s *AdminService) SetClusterJoinTokenStore(store kvstore.AtomicKVStore, trustDomain string) {
	s.joinToken.mu.Lock()
	defer s.joinToken.mu.Unlock()
	s.joinToken.store = store
	s.joinToken.trustDomain = trustDomain
}

// MintJoinToken implements ClusterJoinTokenMinter.
func (s *AdminService) MintJoinToken(ctx context.Context, nodeID string, ttl time.Duration) (string, time.Time, error) {
	s.joinToken.mu.RLock()
	store, trustDomain := s.joinToken.store, s.joinToken.trustDomain
	s.joinToken.mu.RUnlock()
	if store == nil || trustDomain == "" {
		return "", time.Time{}, &ClusterJoinTokenNotConfiguredError{}
	}
	return clusterenroll.MintJoinToken(ctx, store, trustDomain, nodeID, ttl)
}

var _ ClusterJoinTokenMinter = (*AdminService)(nil)
