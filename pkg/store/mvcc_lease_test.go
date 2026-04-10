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

package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// Lease integration helpers
// ---------------------------------------------------------------------------

// newTestMVCCWithLessor creates an MVCCStateMachine with a lessor attached,
// opens it, and registers cleanup. It returns both the state machine and lessor.
func newTestMVCCWithLessor(t *testing.T) (*MVCCStateMachine, *lease.Lessor) {
	t.Helper()
	dir := t.TempDir()
	lr := lease.NewLessor()
	s := NewMVCCStateMachine(nil)
	s.SetLessor(lr)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(ctx)
	})
	return s, lr
}

// applyLeaseEntries applies a set of encoded commands through the state
// machine's Update method at consecutive indices starting from the given base.
func applyLeaseEntries(t *testing.T, s *MVCCStateMachine, baseIndex uint64, cmds ...[]byte) []sm.Result {
	t.Helper()
	entries := make([]sm.Entry, len(cmds))
	results := make([]sm.Result, len(cmds))
	for i, cmd := range cmds {
		entries[i] = sm.Entry{Index: baseIndex + uint64(i), Cmd: cmd}
	}
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	return results
}

// ---------------------------------------------------------------------------
// SetLessor
// ---------------------------------------------------------------------------

func TestMVCCLease_SetLessor_AttachesLessor(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s.Lessor() != nil {
		t.Fatal("expected nil lessor before SetLessor")
	}

	lr := lease.NewLessor()
	s.SetLessor(lr)

	got := s.Lessor()
	if got == nil {
		t.Fatal("expected non-nil lessor after SetLessor")
	}
	if got != lr {
		t.Fatal("Lessor() returned a different lessor than what was set")
	}
}

func TestMVCCLease_SetLessor_Nil(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)

	if s.Lessor() == nil {
		t.Fatal("expected non-nil lessor")
	}

	// Setting nil should be safe and clear the lessor.
	s.SetLessor(nil)
	if s.Lessor() != nil {
		t.Fatal("expected nil lessor after SetLessor(nil)")
	}
}

// ---------------------------------------------------------------------------
// LeaseGrant
// ---------------------------------------------------------------------------

func TestMVCCLease_LeaseGrant_CreatesLease(t *testing.T) {
	s, lr := newTestMVCCWithLessor(t)

	cmd := protocol.EncodeLeaseGrant(60)
	results := applyLeaseEntries(t, s, 1, cmd)

	leaseID := int64(results[0].Value)
	if leaseID <= 0 {
		t.Fatalf("expected lease ID > 0, got %d", leaseID)
	}

	// Verify the lessor actually tracks the lease.
	info := lr.Lookup(leaseID)
	if info == nil {
		t.Fatalf("lessor does not know about lease %d", leaseID)
	}
	if info.TTL != 60 {
		t.Fatalf("expected TTL 60, got %d", info.TTL)
	}
	if info.GrantedTTL != 60 {
		t.Fatalf("expected GrantedTTL 60, got %d", info.GrantedTTL)
	}
}

func TestMVCCLease_LeaseGrant_InvalidTTL(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// TTL of 0 should cause the lessor to reject the grant.
	cmd := protocol.EncodeLeaseGrant(0)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for TTL=0")
	}
}

func TestMVCCLease_LeaseGrant_InvalidPayload(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Construct a command with OpLeaseGrant but a truncated payload (< 8 bytes).
	cmd := []byte{protocol.OpLeaseGrant, 0x01, 0x02}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease grant payload")
	}
}

func TestMVCCLease_LeaseGrant_NoLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	// Deliberately do NOT set a lessor.
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	cmd := protocol.EncodeLeaseGrant(60)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error when granting lease without lessor")
	}

	var notOpen *engine.NotOpenError
	if !errors.As(err, &notOpen) {
		t.Fatalf("expected *engine.NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// LeaseRevoke
// ---------------------------------------------------------------------------

func TestMVCCLease_LeaseRevoke_NoLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	cmd := protocol.EncodeLeaseRevoke(1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error when revoking lease without lessor")
	}

	var notOpen *engine.NotOpenError
	if !errors.As(err, &notOpen) {
		t.Fatalf("expected *engine.NotOpenError, got %T: %v", err, err)
	}
}

func TestMVCCLease_LeaseRevoke_InvalidPayload(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Construct a command with OpLeaseRevoke but a truncated payload.
	cmd := []byte{protocol.OpLeaseRevoke, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease revoke payload")
	}
}

func TestMVCCLease_LeaseRevoke_NonExistentLease(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Revoke a lease that does not exist should return an error.
	cmd := protocol.EncodeLeaseRevoke(99999)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for revoking non-existent lease")
	}
}

func TestMVCCLease_LeaseRevoke_NoAttachedKeys(t *testing.T) {
	s, lr := newTestMVCCWithLessor(t)

	// Grant a lease but attach no keys.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	// Revoke should succeed with 0 detached keys.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyLeaseEntries(t, s, 2, revokeCmd)

	if revokeResults[0].Value != 0 {
		t.Fatalf("expected 0 detached keys, got %d", revokeResults[0].Value)
	}

	// Lease should be gone.
	if lr.Lookup(leaseID) != nil {
		t.Fatal("expected lease to be revoked")
	}
}

func TestMVCCLease_LeaseRevoke_DeletesAttachedKeys(t *testing.T) {
	s, lr := newTestMVCCWithLessor(t)

	// 1. Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(300)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	// 2. Put a key with the lease.
	putCmd := protocol.EncodeMVCCPut([]byte("session/abc"), []byte("alive"), leaseID)
	applyLeaseEntries(t, s, 2, putCmd)

	// Verify key exists.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("session/abc")})
	if err != nil {
		t.Fatalf("expected key to exist: %v", err)
	}
	kv := val.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("alive")) {
		t.Fatalf("expected value 'alive', got %q", kv.Value)
	}

	// Verify key is attached to the lease.
	info := lr.Lookup(leaseID)
	if info == nil {
		t.Fatal("lease should exist before revoke")
	}
	if len(info.Keys) != 1 {
		t.Fatalf("expected 1 attached key, got %d", len(info.Keys))
	}

	// 3. Revoke the lease.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyLeaseEntries(t, s, 3, revokeCmd)

	// Result.Value should contain the count of detached keys.
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 detached key, got %d", revokeResults[0].Value)
	}

	// 4. Verify the key is deleted (MVCC returns an error for deleted keys).
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("session/abc")})
	if err == nil {
		t.Fatal("expected error for deleted key after lease revoke")
	}

	// 5. Verify the lease is gone.
	if lr.Lookup(leaseID) != nil {
		t.Fatal("expected lease to be revoked")
	}
}

// ---------------------------------------------------------------------------
// LeaseKeepAlive
// ---------------------------------------------------------------------------

func TestMVCCLease_LeaseKeepAlive_ResetsExpiry(t *testing.T) {
	s, lr := newTestMVCCWithLessor(t)

	// Grant a lease with a short TTL.
	grantCmd := protocol.EncodeLeaseGrant(10)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	infoBefore := lr.Lookup(leaseID)
	if infoBefore == nil {
		t.Fatal("lease not found after grant")
	}

	// Keep alive.
	keepAliveCmd := protocol.EncodeLeaseKeepAlive(leaseID)
	keepAliveResults := applyLeaseEntries(t, s, 2, keepAliveCmd)

	if int64(keepAliveResults[0].Value) != leaseID {
		t.Fatalf("expected keep-alive to return lease ID %d, got %d", leaseID, keepAliveResults[0].Value)
	}

	infoAfter := lr.Lookup(leaseID)
	if infoAfter == nil {
		t.Fatal("lease not found after keep-alive")
	}

	// Remaining TTL should be close to the full TTL after keep-alive.
	if infoAfter.RemainingTTL < 8 {
		t.Fatalf("expected remaining TTL >= 8 after keep-alive, got %d", infoAfter.RemainingTTL)
	}
}

func TestMVCCLease_LeaseKeepAlive_NonExistentLease(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// KeepAlive on a lease that does not exist should return an error.
	cmd := protocol.EncodeLeaseKeepAlive(99999)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for keep-alive on non-existent lease")
	}
}

func TestMVCCLease_LeaseKeepAlive_InvalidPayload(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Construct a command with OpLeaseKeepAlive but a truncated payload.
	cmd := []byte{protocol.OpLeaseKeepAlive, 0x01, 0x02, 0x03}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease keep-alive payload")
	}
}

func TestMVCCLease_LeaseKeepAlive_NoLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	cmd := protocol.EncodeLeaseKeepAlive(999)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error when keep-alive without lessor")
	}

	var notOpen *engine.NotOpenError
	if !errors.As(err, &notOpen) {
		t.Fatalf("expected *engine.NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Lookup: LeaseTimeToLiveQuery
// ---------------------------------------------------------------------------

func TestMVCCLease_Lookup_LeaseTimeToLive(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(120)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	// Lookup using LeaseTimeToLiveQuery.
	result, err := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		t.Fatalf("lookup lease TTL: %v", err)
	}

	l, ok := result.(*service.Lease)
	if !ok {
		t.Fatalf("expected *service.Lease, got %T", result)
	}
	if l.ID != leaseID {
		t.Fatalf("expected lease ID %d, got %d", leaseID, l.ID)
	}
	if l.TTL != 120 {
		t.Fatalf("expected TTL 120, got %d", l.TTL)
	}
	if l.GrantedTTL != 120 {
		t.Fatalf("expected GrantedTTL 120, got %d", l.GrantedTTL)
	}
	if l.RemainingTTL <= 0 {
		t.Fatal("expected positive remaining TTL")
	}
}

func TestMVCCLease_Lookup_LeaseTimeToLive_WithKeys(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Grant a lease and attach keys.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	putCmd1 := protocol.EncodeMVCCPut([]byte("key1"), []byte("val1"), leaseID)
	putCmd2 := protocol.EncodeMVCCPut([]byte("key2"), []byte("val2"), leaseID)
	applyLeaseEntries(t, s, 2, putCmd1, putCmd2)

	// Lookup should include keys.
	result, err := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		t.Fatalf("lookup lease TTL: %v", err)
	}

	l := result.(*service.Lease)
	if len(l.Keys) != 2 {
		t.Fatalf("expected 2 attached keys, got %d", len(l.Keys))
	}

	// Verify both keys are present (order is not guaranteed).
	keySet := make(map[string]struct{})
	for _, k := range l.Keys {
		keySet[string(k)] = struct{}{}
	}
	if _, ok := keySet["key1"]; !ok {
		t.Fatal("expected key1 in lease keys")
	}
	if _, ok := keySet["key2"]; !ok {
		t.Fatal("expected key2 in lease keys")
	}
}

// ---------------------------------------------------------------------------
// Lookup: LeasesQuery
// ---------------------------------------------------------------------------

func TestMVCCLease_Lookup_LeasesQuery(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// Grant multiple leases.
	grantCmd1 := protocol.EncodeLeaseGrant(30)
	grantCmd2 := protocol.EncodeLeaseGrant(60)
	grantCmd3 := protocol.EncodeLeaseGrant(90)
	results := applyLeaseEntries(t, s, 1, grantCmd1, grantCmd2, grantCmd3)

	leaseIDs := make(map[int64]struct{})
	for _, r := range results {
		leaseIDs[int64(r.Value)] = struct{}{}
	}

	// Lookup all leases.
	result, err := s.Lookup(context.Background(), service.LeasesQuery{})
	if err != nil {
		t.Fatalf("lookup leases: %v", err)
	}

	leases, ok := result.([]*service.Lease)
	if !ok {
		t.Fatalf("expected []*service.Lease, got %T", result)
	}
	if len(leases) != 3 {
		t.Fatalf("expected 3 leases, got %d", len(leases))
	}

	// Verify all lease IDs are present.
	for _, l := range leases {
		if _, ok := leaseIDs[l.ID]; !ok {
			t.Fatalf("unexpected lease ID %d in results", l.ID)
		}
		delete(leaseIDs, l.ID)
	}
	if len(leaseIDs) != 0 {
		t.Fatalf("missing lease IDs: %v", leaseIDs)
	}
}

func TestMVCCLease_Lookup_LeasesQuery_NoLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	_, err = s.Lookup(context.Background(), service.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error for leases lookup without lessor")
	}

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCLease_Lookup_LeasesQuery_Empty(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	// No leases granted.
	result, err := s.Lookup(context.Background(), service.LeasesQuery{})
	if err != nil {
		t.Fatalf("lookup leases: %v", err)
	}

	leases, ok := result.([]*service.Lease)
	if !ok {
		t.Fatalf("expected []*service.Lease, got %T", result)
	}
	if len(leases) != 0 {
		t.Fatalf("expected 0 leases, got %d", len(leases))
	}
}

// ---------------------------------------------------------------------------
// Lookup: lease not found
// ---------------------------------------------------------------------------

func TestMVCCLease_Lookup_LeaseNotFound(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)

	_, err := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: 999999})
	if err == nil {
		t.Fatal("expected error for non-existent lease")
	}

	var iqe2 *engine.InvalidQueryError
	if !errors.As(err, &iqe2) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCLease_Lookup_LeaseNotFound_NoLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	_, err = s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: 1})
	if err == nil {
		t.Fatal("expected error for lease lookup without lessor")
	}

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Put: attaches key to lease
// ---------------------------------------------------------------------------

func TestMVCCLease_Put_AttachesKeyToLease(t *testing.T) {
	s, lr := newTestMVCCWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	// Put a key with the lease.
	putCmd := protocol.EncodeMVCCPut([]byte("mykey"), []byte("myval"), leaseID)
	applyLeaseEntries(t, s, 2, putCmd)

	// Verify the key is attached to the lease.
	info := lr.Lookup(leaseID)
	if info == nil {
		t.Fatal("lease not found")
	}
	if len(info.Keys) != 1 {
		t.Fatalf("expected 1 key attached to lease, got %d", len(info.Keys))
	}
	if !bytes.Equal(info.Keys[0], []byte("mykey")) {
		t.Fatalf("expected attached key 'mykey', got %q", info.Keys[0])
	}

	// Verify the key value through lookup.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("mykey")})
	if err != nil {
		t.Fatalf("lookup key: %v", err)
	}
	kv := val.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("myval")) {
		t.Fatalf("expected value 'myval', got %q", kv.Value)
	}
	if kv.LeaseID != leaseID {
		t.Fatalf("expected leaseID %d on key, got %d", leaseID, kv.LeaseID)
	}
}

func TestMVCCLease_Put_AttachesMultipleKeysToLease(t *testing.T) {
	s, lr := newTestMVCCWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(120)
	grantResults := applyLeaseEntries(t, s, 1, grantCmd)
	leaseID := int64(grantResults[0].Value)

	// Put multiple keys with the same lease.
	putCmd1 := protocol.EncodeMVCCPut([]byte("a"), []byte("1"), leaseID)
	putCmd2 := protocol.EncodeMVCCPut([]byte("b"), []byte("2"), leaseID)
	putCmd3 := protocol.EncodeMVCCPut([]byte("c"), []byte("3"), leaseID)
	applyLeaseEntries(t, s, 2, putCmd1, putCmd2, putCmd3)

	info := lr.Lookup(leaseID)
	if info == nil {
		t.Fatal("lease not found")
	}
	if len(info.Keys) != 3 {
		t.Fatalf("expected 3 keys attached to lease, got %d", len(info.Keys))
	}

	keySet := make(map[string]struct{})
	for _, k := range info.Keys {
		keySet[string(k)] = struct{}{}
	}
	for _, expected := range []string{"a", "b", "c"} {
		if _, ok := keySet[expected]; !ok {
			t.Fatalf("expected key %q in lease keys", expected)
		}
	}
}
