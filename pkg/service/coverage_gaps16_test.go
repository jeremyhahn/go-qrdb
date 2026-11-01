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

// coverage_gaps16_test.go covers the final ~32 statement gaps.
package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Rotate KV put error paths (lines 360-378)
// ---------------------------------------------------------------------------

// failAfterNKV is a KV that starts failing Puts after N successful ones.
// It wraps a normal afMockKV and delegates until putFailAfter.
type failAfterNKV struct {
	*afMockKV
	failAfter int
	count     int
}

func newFailAfterNKV(failAfter int) *failAfterNKV {
	return &failAfterNKV{afMockKV: newAFMockKV(), failAfter: failAfter}
}

func (f *failAfterNKV) Put(ctx context.Context, key string, val []byte) error {
	f.count++
	if f.count > f.failAfter {
		return errors.New("kv: put failed")
	}
	return f.afMockKV.Put(ctx, key, val)
}

// PutIfAbsent counts a factor's primary record as Put does: the record is
// reserved behind an absence guard, and it is the same write. The factor-set
// commit is not counted.
func (f *failAfterNKV) PutIfAbsent(ctx context.Context, key string, val []byte) (bool, error) {
	if strings.HasPrefix(key, userFactorPrimaryPrefix) {
		f.count++
		if f.count > f.failAfter {
			return false, errors.New("kv: put failed")
		}
	}
	return f.afMockKV.PutIfAbsent(ctx, key, val)
}

// makeAuthSvcWithKV creates a UserAuthFactorServicer with the given KV.
func makeAuthSvcWithKV(t *testing.T, kv KVServicer) UserAuthFactorServicer {
	t.Helper()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

// TestUserAuthFactorService_Rotate_NewFactorPrimaryKVFail tests line 360-362.
// During Rotate, the first Put is for the newFactor primary key.
// The Add for oldFactor uses 3 puts. After that, Rotate uses more puts.
func TestUserAuthFactorService_Rotate_PrimaryKVFail(t *testing.T) {
	// Add requires 3 puts. Set failAfter=3 so adds succeed but the first
	// Rotate put (4th put) fails.
	kv := newFailAfterNKV(3)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(5000)

	// Add old factor (uses 3 puts successfully).
	oldF := mtlsFactor("old-pk", userID, []byte("old-cert"))
	if err := svc.Add(ctx, userID, oldF); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Rotate: 4th Put fails → newFactor primary key put fails.
	newF := &auth.AuthFactor{
		Type:       auth.AuthFactorMTLS,
		UserID:     userID,
		Credential: []byte("new-cert"),
	}
	err := svc.Rotate(ctx, oldF.ID, newF)
	if err == nil {
		t.Fatal("expected error on newFactor primary put failure, got nil")
	}
}

func TestUserAuthFactorService_Rotate_UserIndexKVFail(t *testing.T) {
	// 3 puts for Add, 5th put (2nd in Rotate) fails = userIndex put.
	kv := newFailAfterNKV(4)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(5001)

	oldF := mtlsFactor("old-ui", userID, []byte("old-cert-ui"))
	if err := svc.Add(ctx, userID, oldF); err != nil {
		t.Fatalf("Add: %v", err)
	}

	newF := &auth.AuthFactor{
		Type:       auth.AuthFactorMTLS,
		UserID:     userID,
		Credential: []byte("new-cert-ui"),
	}
	err := svc.Rotate(ctx, oldF.ID, newF)
	if err == nil {
		t.Fatal("expected error on userIndex put failure, got nil")
	}
}

func TestUserAuthFactorService_Rotate_LookupKVFail(t *testing.T) {
	// 3 puts for Add, 6th put (3rd in Rotate) fails = lookupKey put.
	kv := newFailAfterNKV(5)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(5002)

	oldF := mtlsFactor("old-lk", userID, []byte("old-cert-lk"))
	if err := svc.Add(ctx, userID, oldF); err != nil {
		t.Fatalf("Add: %v", err)
	}

	newF := &auth.AuthFactor{
		Type:       auth.AuthFactorMTLS,
		UserID:     userID,
		Credential: []byte("new-cert-lk"),
	}
	err := svc.Rotate(ctx, oldF.ID, newF)
	if err == nil {
		t.Fatal("expected error on lookupKey put failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Rotate: already revoked old factor
// Line 324-326
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Rotate_OldFactorRevoked(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	userID := uint64(6000)

	// Add two factors (need 2 for force removal to be allowed).
	f1 := mtlsFactor("f1-rev-test", userID, []byte("cert1"))
	f2 := pivFactor("f2-rev-test", userID, []byte("cert2"))
	if err := svc.Add(ctx, userID, f1); err != nil {
		t.Fatalf("Add f1: %v", err)
	}
	if err := svc.Add(ctx, userID, f2); err != nil {
		t.Fatalf("Add f2: %v", err)
	}

	// Revoke f1 with force=true.
	if err := svc.Remove(ctx, f1.ID, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Try to rotate f1 (already revoked).
	newF := &auth.AuthFactor{
		Type:       auth.AuthFactorMTLS,
		UserID:     userID,
		Credential: []byte("new-cert"),
	}
	err := svc.Rotate(ctx, f1.ID, newF)
	if err == nil {
		t.Fatal("expected error for rotating revoked factor, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Remove: KV Put error (line 295-297)
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Remove_KVPutFails(t *testing.T) {
	// Add requires 3 puts. Force Remove uses 1 put (update revoked factor).
	// Fail on the 4th put.
	kv := newFailAfterNKV(3)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(7000)

	// Add two factors so force remove is allowed.
	f1 := mtlsFactor("f1-rm-kv", userID, []byte("c1"))
	f2 := pivFactor("f2-rm-kv", userID, []byte("c2"))
	// These use puts 1-3 and 4-6 respectively... but kv has failAfter=3.
	// After 3 puts, Puts fail. So f2.Add will fail.
	// Instead: use failAfter=6 to let both adds succeed.
	kv2 := newFailAfterNKV(6)
	svc2 := makeAuthSvcWithKV(t, kv2)
	if err := svc2.Add(ctx, userID, f1); err != nil {
		t.Fatalf("Add f1: %v", err)
	}
	if err := svc2.Add(ctx, userID, f2); err != nil {
		t.Fatalf("Add f2: %v", err)
	}
	// Now kv2.count == 6, next Put fails.

	// Remove f1 with force — 7th Put should fail.
	err := svc2.Remove(ctx, f1.ID, true)
	if err == nil {
		t.Fatal("expected error on remove KV put failure, got nil")
	}
	_ = svc
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – LookupByCredentialKey error paths (86.7%)
// Lines 250-252, 253-255
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_LookupByCredentialKey_NotFound(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()

	// Look up a key that was never enrolled.
	_, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("no-such-cert"))
	if err == nil {
		t.Fatal("expected error for unknown credential, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – MarkUsed success path (line 416-418, 426-428)
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_MarkUsed_FactorFound(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	userID := uint64(8000)

	f := mtlsFactor("f-markused", userID, []byte("cert-mu"))
	if err := svc.Add(ctx, userID, f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// MarkUsed on a known factor.
	err := svc.MarkUsed(ctx, f.ID, nowTime())
	if err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
}

func nowTime() time.Time { return time.Now() }

// ---------------------------------------------------------------------------
// Imports
// ---------------------------------------------------------------------------

var (
	_ = errors.New
	_ = auth.AuthFactorMTLS
	_ = strings.HasPrefix
)
