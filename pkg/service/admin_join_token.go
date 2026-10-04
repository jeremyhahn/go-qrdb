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
	"math"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// MaxJoinTokenTTLSeconds is the largest wire-supplied ttl_seconds value
// ParseJoinTokenTTLSeconds accepts: the largest value that, multiplied by
// time.Second, does not overflow int64 nanoseconds. It is far larger than
// clusterenroll.MaxJoinTokenTTL (24h = 86400s) -- that range check still
// decides what counts as "too long" for a join token; this guards a
// different failure, the raw arithmetic overflow that would otherwise
// silently wrap a huge ttl_seconds into a small or negative
// time.Duration before clusterenroll.MintJoinToken ever sees it.
const MaxJoinTokenTTLSeconds = math.MaxInt64 / int64(time.Second)

// ParseJoinTokenTTLSeconds converts a wire-supplied ttl_seconds value --
// every transport's MintJoinToken request carries one as a plain int64 --
// to a time.Duration, refusing a value whose conversion would overflow
// instead of silently wrapping. Every transport binding (gRPC, QUIC, MCP,
// REST) calls this before AdminService.MintJoinToken rather than
// converting inline, so the guard cannot be present on some transports and
// missing on others.
//
// ttlSeconds == 0 converts to 0, which MintJoinToken (via
// clusterenroll.MintJoinToken) already treats as "use the default TTL" --
// this function does not duplicate that default, it only guards the
// multiplication. A NEGATIVE ttlSeconds is deliberately NOT collapsed to 0
// here: it converts to a negative time.Duration and is passed through
// unchanged, so clusterenroll.MintJoinToken's own refusal
// (*clusterenroll.JoinTokenTTLError) is what every transport sees for it,
// identically -- this function silently defaulting a negative value here
// would hide a caller bug (a bad subtraction, an unconverted unit) behind
// an ordinary-looking token on some transports and not others, depending
// on whether they happened to also validate it themselves.
func ParseJoinTokenTTLSeconds(ttlSeconds int64) (time.Duration, error) {
	if ttlSeconds > MaxJoinTokenTTLSeconds {
		return 0, &JoinTokenTTLOverflowError{TTLSeconds: ttlSeconds}
	}
	if ttlSeconds < -MaxJoinTokenTTLSeconds {
		return 0, &JoinTokenTTLOverflowError{TTLSeconds: ttlSeconds}
	}
	if ttlSeconds == 0 {
		return 0, nil
	}
	return time.Duration(ttlSeconds) * time.Second, nil
}

// JoinTokenTTLOverflowError is returned by ParseJoinTokenTTLSeconds when a
// wire-supplied ttl_seconds value is too large to convert to a
// time.Duration without overflowing int64 nanoseconds.
type JoinTokenTTLOverflowError struct {
	TTLSeconds int64
}

// Error returns a human-readable description.
func (e *JoinTokenTTLOverflowError) Error() string {
	return "service: ttl_seconds exceeds the maximum convertible to a duration"
}

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
