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

package statemachine

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// mockLeaseManager: test double for LeaseManager
// ---------------------------------------------------------------------------

type mockLeaseManager struct {
	grantFn     func(ttl int64) (int64, int64, error)
	revokeFn    func(leaseID int64) error
	keepAliveFn func(leaseID int64) (int64, error)
	ttlFn       func(leaseID int64) (interface{}, error)
	leasesFn    func() (interface{}, error)
}

func (m *mockLeaseManager) Grant(ttl int64) (int64, int64, error) {
	if m.grantFn != nil {
		return m.grantFn(ttl)
	}
	return 0, 0, nil
}

func (m *mockLeaseManager) Revoke(leaseID int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(leaseID)
	}
	return nil
}

func (m *mockLeaseManager) KeepAlive(leaseID int64) (int64, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(leaseID)
	}
	return 0, nil
}

func (m *mockLeaseManager) TimeToLive(leaseID int64) (interface{}, error) {
	if m.ttlFn != nil {
		return m.ttlFn(leaseID)
	}
	return nil, nil
}

func (m *mockLeaseManager) Leases() (interface{}, error) {
	if m.leasesFn != nil {
		return m.leasesFn()
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Tests: lease write handlers
// ---------------------------------------------------------------------------

func TestLeaseGrant_NilLeaseManager(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := protocol.EncodeLeaseGrant(60)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention %q, got %q", "lease", results[0].Data)
	}
}

func TestLeaseGrant_Success(t *testing.T) {
	var capturedTTL int64
	lm := &mockLeaseManager{
		grantFn: func(ttl int64) (int64, int64, error) {
			capturedTTL = ttl
			return 42, ttl, nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := protocol.EncodeLeaseGrant(300)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if capturedTTL != 300 {
		t.Fatalf("expected TTL 300, got %d", capturedTTL)
	}
	if results[0].Value != 42 {
		t.Fatalf("expected result value 42 (lease ID), got %d", results[0].Value)
	}
}

func TestLeaseGrant_InvalidPayload(t *testing.T) {
	lm := &mockLeaseManager{}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	// Truncated payload: op byte + only 4 bytes (need 8 for TTL).
	cmd := []byte{protocol.OpLeaseGrant, 0x01, 0x02, 0x03, 0x04}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated lease grant payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestLeaseGrant_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		grantFn: func(_ int64) (int64, int64, error) {
			return 0, 0, &lease.InvalidTTLError{TTL: -1}
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := protocol.EncodeLeaseGrant(-1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from lease manager")
	}

	var ttlErr *lease.InvalidTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("expected InvalidTTLError, got %T: %v", err, err)
	}
}

func TestLeaseRevoke_NilLeaseManager(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := protocol.EncodeLeaseRevoke(1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention %q, got %q", "lease", results[0].Data)
	}
}

func TestLeaseRevoke_Success(t *testing.T) {
	var capturedID int64
	lm := &mockLeaseManager{
		revokeFn: func(leaseID int64) error {
			capturedID = leaseID
			return nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := protocol.EncodeLeaseRevoke(99)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if capturedID != 99 {
		t.Fatalf("expected lease ID 99, got %d", capturedID)
	}
	// Result value should be the revision (non-zero after Update).
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from revoke")
	}
}

func TestLeaseRevoke_InvalidPayload(t *testing.T) {
	lm := &mockLeaseManager{}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := []byte{protocol.OpLeaseRevoke, 0x01, 0x02}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated lease revoke payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestLeaseRevoke_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		revokeFn: func(leaseID int64) error {
			return &lease.NotFoundError{ID: leaseID}
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := protocol.EncodeLeaseRevoke(999)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from lease manager")
	}

	var nfErr *lease.NotFoundError
	if !errors.As(err, &nfErr) {
		t.Fatalf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestLeaseKeepAlive_NilLeaseManager(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := protocol.EncodeLeaseKeepAlive(1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention %q, got %q", "lease", results[0].Data)
	}
}

func TestLeaseKeepAlive_Success(t *testing.T) {
	var capturedID int64
	lm := &mockLeaseManager{
		keepAliveFn: func(leaseID int64) (int64, error) {
			capturedID = leaseID
			return 60, nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := protocol.EncodeLeaseKeepAlive(7)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if capturedID != 7 {
		t.Fatalf("expected lease ID 7, got %d", capturedID)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from keep-alive")
	}
}

func TestLeaseKeepAlive_InvalidPayload(t *testing.T) {
	lm := &mockLeaseManager{}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := []byte{protocol.OpLeaseKeepAlive, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated lease keep-alive payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestLeaseKeepAlive_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		keepAliveFn: func(leaseID int64) (int64, error) {
			return 0, &lease.NotFoundError{ID: leaseID}
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	cmd := protocol.EncodeLeaseKeepAlive(404)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from lease manager")
	}

	var nfErr *lease.NotFoundError
	if !errors.As(err, &nfErr) {
		t.Fatalf("expected NotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: lease lookup handlers
// ---------------------------------------------------------------------------

func TestLookupLeaseTimeToLive_NilLeaseManager(t *testing.T) {
	fsm, _ := newTestFSM()

	_, err := fsm.Lookup(context.Background(), &lease.LeaseTimeToLiveQuery{ID: 1})
	if err == nil {
		t.Fatal("expected error when lease manager is nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestLookupLeaseTimeToLive_Success(t *testing.T) {
	expected := &lease.Lease{
		ID:           5,
		TTL:          120,
		GrantedTTL:   120,
		RemainingTTL: 90,
	}
	lm := &mockLeaseManager{
		ttlFn: func(leaseID int64) (interface{}, error) {
			if leaseID != 5 {
				t.Fatalf("expected lease ID 5, got %d", leaseID)
			}
			return expected, nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	result, err := fsm.Lookup(context.Background(), &lease.LeaseTimeToLiveQuery{ID: 5})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	got, ok := result.(*lease.Lease)
	if !ok {
		t.Fatalf("expected *lease.Lease, got %T", result)
	}
	if got.ID != 5 || got.TTL != 120 {
		t.Fatalf("unexpected lease: %+v", got)
	}
}

func TestLookupLeaseTimeToLive_ValueType(t *testing.T) {
	// Test with value type (not pointer) query.
	lm := &mockLeaseManager{
		ttlFn: func(leaseID int64) (interface{}, error) {
			return &lease.Lease{ID: leaseID, TTL: 30}, nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	result, err := fsm.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 10})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	got, ok := result.(*lease.Lease)
	if !ok {
		t.Fatalf("expected *lease.Lease, got %T", result)
	}
	if got.ID != 10 {
		t.Fatalf("expected lease ID 10, got %d", got.ID)
	}
}

func TestLookupLeaseTimeToLive_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		ttlFn: func(leaseID int64) (interface{}, error) {
			return nil, &lease.NotFoundError{ID: leaseID}
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	_, err := fsm.Lookup(context.Background(), &lease.LeaseTimeToLiveQuery{ID: 999})
	if err == nil {
		t.Fatal("expected error from lease manager")
	}

	var nfErr *lease.NotFoundError
	if !errors.As(err, &nfErr) {
		t.Fatalf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestLookupLeases_NilLeaseManager(t *testing.T) {
	fsm, _ := newTestFSM()

	_, err := fsm.Lookup(context.Background(), &lease.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error when lease manager is nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestLookupLeases_Success(t *testing.T) {
	expected := []*lease.Lease{
		{ID: 1, TTL: 60},
		{ID: 2, TTL: 120},
	}
	lm := &mockLeaseManager{
		leasesFn: func() (interface{}, error) {
			return expected, nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	result, err := fsm.Lookup(context.Background(), &lease.LeasesQuery{})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	got, ok := result.([]*lease.Lease)
	if !ok {
		t.Fatalf("expected []*lease.Lease, got %T", result)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(got))
	}
}

func TestLookupLeases_ValueType(t *testing.T) {
	// Test with value type (not pointer) query.
	lm := &mockLeaseManager{
		leasesFn: func() (interface{}, error) {
			return []*lease.Lease{{ID: 1}}, nil
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	result, err := fsm.Lookup(context.Background(), lease.LeasesQuery{})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	got, ok := result.([]*lease.Lease)
	if !ok {
		t.Fatalf("expected []*lease.Lease, got %T", result)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(got))
	}
}

func TestLookupLeases_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		leasesFn: func() (interface{}, error) {
			return nil, &lease.LessorClosedError{}
		},
	}
	fsm, _ := newTestFSM(WithLeaseManager(lm))

	_, err := fsm.Lookup(context.Background(), &lease.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error from lease manager")
	}

	var closedErr *lease.LessorClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected LessorClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: error types
// ---------------------------------------------------------------------------

func TestServiceNotConfiguredError_Error(t *testing.T) {
	err := &ServiceNotConfiguredError{Service: "lease"}
	msg := err.Error()
	expected := "statemachine: lease not configured"
	if msg != expected {
		t.Fatalf("expected %q, got %q", expected, msg)
	}
}

func TestServiceNotConfiguredError_DifferentService(t *testing.T) {
	err := &ServiceNotConfiguredError{Service: "index"}
	msg := err.Error()
	expected := "statemachine: index not configured"
	if msg != expected {
		t.Fatalf("expected %q, got %q", expected, msg)
	}
}

// ---------------------------------------------------------------------------
// Tests: WithLeaseManager option
// ---------------------------------------------------------------------------

func TestWithLeaseManager_Sets(t *testing.T) {
	lm := &mockLeaseManager{}
	fsm, _ := newTestFSM(WithLeaseManager(lm))
	if fsm.leaseManager == nil {
		t.Fatal("expected leaseManager to be set")
	}
}

func TestWithLeaseManager_NilByDefault(t *testing.T) {
	fsm, _ := newTestFSM()
	if fsm.leaseManager != nil {
		t.Fatal("expected leaseManager to be nil by default")
	}
}
