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

package cluster

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// errElecting stands in for the system shard electing a leader: the error a
// registration proposed into the election answers with.
var errElecting = errors.New("test: not leader for shard 1, leader unknown")

// electingStore answers its first electing PutMember calls with errElecting,
// as a shard mid-election does, and every later one through fakeStore. A
// non-nil refusal answers every call instead.
type electingStore struct {
	*fakeStore
	electing int64
	refusal  error
	calls    atomic.Int64
}

func (s *electingStore) PutMember(ctx context.Context, nodeID uint64, data []byte, ttl time.Duration) error {
	n := s.calls.Add(1)
	if s.refusal != nil {
		return s.refusal
	}
	if s.electing < 0 || n <= s.electing {
		return errElecting
	}
	return s.fakeStore.PutMember(ctx, nodeID, data, ttl)
}

// electionSettler is a Settler that retries attempt while it answers
// errElecting, up to attempts times, as service.SettleLeadership does within
// its budget for service.LeadershipTransient.
func electionSettler(attempts int) Settler {
	return func(ctx context.Context, attempt func(context.Context) error) error {
		var err error
		for range attempts {
			if err = attempt(ctx); !errors.Is(err, errElecting) || ctx.Err() != nil {
				return err
			}
		}
		return err
	}
}

// registerSelfError asserts err is the registration failure a boot reports,
// caused by want.
func registerSelfError(t *testing.T, err, want error) {
	t.Helper()
	var cause *MembershipCauseError
	if !errors.As(err, &cause) || cause.Reason != "failed to register self" {
		t.Fatalf("Register = %v, want a \"failed to register self\" *MembershipCauseError", err)
	}
	if !errors.Is(err, want) {
		t.Fatalf("Register = %v, want it caused by %v", err, want)
	}
}

func newSettleAdvertiser(t *testing.T, store MembershipStore, opts ...AdvertiseOption) *Advertiser {
	t.Helper()
	adv, err := NewAdvertiser(store, selfMember(), AdvertiseConfig{LeaseTTL: time.Second, KeepAliveInterval: time.Hour}, opts...)
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}
	return adv
}

func TestAdvertiserRegister_WaitsOutElection(t *testing.T) {
	store := &electingStore{fakeStore: newFakeStore(), electing: 3}
	adv := newSettleAdvertiser(t, store, WithAdvertiseSettler(electionSettler(10)))

	if err := adv.Register(context.Background()); err != nil {
		t.Fatalf("Register through an election = %v, want nil", err)
	}
	if got := store.calls.Load(); got != 4 {
		t.Fatalf("PutMember calls = %d, want 3 elections and the write", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.putCalls != 1 || len(store.puts) != 1 || store.puts[selfMember().NodeID] == nil {
		t.Fatalf("store holds %d records after %d writes, want this node's one", len(store.puts), store.putCalls)
	}
}

func TestAdvertiserRegister_ElectionOutlastsSettler(t *testing.T) {
	store := &electingStore{fakeStore: newFakeStore(), electing: -1}
	adv := newSettleAdvertiser(t, store, WithAdvertiseSettler(electionSettler(3)))

	registerSelfError(t, adv.Register(context.Background()), errElecting)
	if got := store.calls.Load(); got != 3 {
		t.Fatalf("PutMember calls = %d, want the settler's 3", got)
	}
}

func TestAdvertiserRegister_RefusalIsNotRetried(t *testing.T) {
	refusal := errors.New("transport: unsupported capability: statemachine: lease not configured")
	store := &electingStore{fakeStore: newFakeStore(), refusal: refusal}
	adv := newSettleAdvertiser(t, store, WithAdvertiseSettler(electionSettler(10)))

	registerSelfError(t, adv.Register(context.Background()), refusal)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("PutMember calls = %d, want a refusal answered once", got)
	}
}

func TestAdvertiserRegister_WithoutSettlerOneAttempt(t *testing.T) {
	for name, opts := range map[string][]AdvertiseOption{
		"no settler":  nil,
		"nil settler": {WithAdvertiseSettler(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			store := &electingStore{fakeStore: newFakeStore(), electing: 1}
			adv := newSettleAdvertiser(t, store, opts...)

			registerSelfError(t, adv.Register(context.Background()), errElecting)
			if got := store.calls.Load(); got != 1 {
				t.Fatalf("PutMember calls = %d, want one attempt", got)
			}
		})
	}
}

func TestAdvertiserRegister_SettlerGetsRegistrationContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "boot")
	var seen context.Context
	adv := newSettleAdvertiser(t, newFakeStore(), WithAdvertiseSettler(func(sctx context.Context, attempt func(context.Context) error) error {
		seen = sctx
		return attempt(sctx)
	}))

	if err := adv.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if seen == nil || seen.Value(ctxKey{}) != "boot" {
		t.Fatal("the settler did not get the registration's context")
	}
}

func TestSettleOnce(t *testing.T) {
	calls := 0
	if err := settleOnce(context.Background(), func(context.Context) error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("settleOnce = %v after %d calls, want nil after 1", err, calls)
	}
	calls = 0
	if err := settleOnce(context.Background(), func(context.Context) error { calls++; return errElecting }); !errors.Is(err, errElecting) || calls != 1 {
		t.Fatalf("settleOnce = %v after %d calls, want %v after 1", err, calls, errElecting)
	}
}

func TestMembershipManager_StartWaitsOutElection(t *testing.T) {
	store := &electingStore{fakeStore: newFakeStore(), electing: 2}
	m, err := NewMembershipManager(store, selfMember(), 100,
		MembershipConfig{LeaseTTL: time.Second, KeepAliveInterval: time.Hour},
		WithMembershipSettler(electionSettler(10)))
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start through an election = %v, want nil", err)
	}
	if got := store.calls.Load(); got != 3 {
		t.Fatalf("PutMember calls = %d, want 2 elections and the write", got)
	}
}

func TestMembershipManager_StartWithoutSettlerFailsOnElection(t *testing.T) {
	store := &electingStore{fakeStore: newFakeStore(), electing: 1}
	m, err := NewMembershipManager(store, selfMember(), 100,
		MembershipConfig{LeaseTTL: time.Second, KeepAliveInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	registerSelfError(t, m.Start(context.Background()), errElecting)
	// The failed start stays retryable, and the election is over.
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("retry Start: %v", err)
	}
}
