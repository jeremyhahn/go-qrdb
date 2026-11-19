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

package lock

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// stampedLockValue acquires a lock through a Service built with opts and returns
// the record the Service wrote for it.
func stampedLockValue(t *testing.T, opts ...Option) lockValue {
	t.Helper()
	var written []byte
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, ops []Op, _ []Op) (*TransactionResponse, error) {
			written = ops[0].Value
			return &TransactionResponse{Succeeded: true, Revision: 1}, nil
		},
	}
	ls := &mockLeaseService{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{ID: 7, TTL: ttl, GrantedTTL: ttl}, nil
		},
	}
	svc, err := NewLocker(tx, ls, &mockWatchService{}, &mockMVCCKVService{}, nil, opts...)
	if err != nil {
		t.Fatalf("NewLocker: %v", err)
	}
	if _, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 30 * time.Second}); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	var lv lockValue
	if err := json.Unmarshal(written, &lv); err != nil {
		t.Fatalf("the lock record is not valid JSON: %v", err)
	}
	return lv
}

// TestTryLock_StampsAcquiredAtFromTheClock verifies the acquisition time is the
// injected clock's, so a test that drives the lease and the lock from one fake
// sees the two agree.
func TestTryLock_StampsAcquiredAtFromTheClock(t *testing.T) {
	fake := clocktest.New()
	fake.Advance(90 * time.Second)

	lv := stampedLockValue(t, WithClock(fake))

	if want := fake.Now().Unix(); lv.AcquiredAt != want {
		t.Fatalf("AcquiredAt = %d, want the fake's %d", lv.AcquiredAt, want)
	}
}

func TestTryLock_AcquiredAtFollowsTheClockAcrossAcquisitions(t *testing.T) {
	fake := clocktest.New()
	first := stampedLockValue(t, WithClock(fake))
	fake.Advance(time.Hour)
	second := stampedLockValue(t, WithClock(fake))

	if got := second.AcquiredAt - first.AcquiredAt; got != int64(time.Hour/time.Second) {
		t.Fatalf("AcquiredAt advanced %ds across an hour of fake time, want 3600", got)
	}
}

func TestNewLocker_DefaultsToTheRealClock(t *testing.T) {
	before := time.Now().Unix()
	lv := stampedLockValue(t)
	if after := time.Now().Unix(); lv.AcquiredAt < before || lv.AcquiredAt > after {
		t.Fatalf("AcquiredAt = %d, want within [%d, %d] of the wall clock", lv.AcquiredAt, before, after)
	}
}

func TestWithClock_NilKeepsTheDefault(t *testing.T) {
	before := time.Now().Unix()
	lv := stampedLockValue(t, WithClock(nil))
	if after := time.Now().Unix(); lv.AcquiredAt < before || lv.AcquiredAt > after {
		t.Fatalf("AcquiredAt = %d, want within [%d, %d] of the wall clock", lv.AcquiredAt, before, after)
	}
}
