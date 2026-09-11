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

// coverage_gaps18_test.go covers the last ~23 missing statements.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Add nil factor (line 150-152)
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Add_NilFactor(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()

	err := svc.Add(ctx, uint64(1), nil)
	if err == nil {
		t.Fatal("expected error for nil factor, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Add: user index Put error (line 168-170)
// The Add function does 3 kv.Puts. With failAfter=1, the 2nd Put fails.
// But: we must pass a factor that doesn't already exist (no lookupFactorID hit).
// After marshal (no Put), the sequence is: primary(1), userIndex(2), lookup(3).
// failAfter=1 → 2nd Put fails = user index.
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Add_UserIndexKVFail_Direct(t *testing.T) {
	kv := newFailAfterNKV(1)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(10001)

	f := mtlsFactor("f-ui-d", userID, []byte("cert-uid"))
	err := svc.Add(ctx, userID, f)
	if err == nil {
		t.Fatal("expected error on user index KV fail, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Add: lookup key Put error (line 181-183)
// failAfter=2 → 3rd Put fails = lookup key.
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Add_LookupKeyKVFail_Direct(t *testing.T) {
	kv := newFailAfterNKV(2)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(10002)

	f := mtlsFactor("f-lk-d", userID, []byte("cert-lkd"))
	err := svc.Add(ctx, userID, f)
	if err == nil {
		t.Fatal("expected error on lookup key KV fail, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – List: Get error path (line 206-208)
// The user index lists a factorID but kv.Get of that factorID fails.
// This happens when the user index key stores a FACTOR ID (not its data).
// lookupKey → factorID (via Get on user index). Then Get(primaryKey(factorID)).
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_List_PrimaryKeyGetFails(t *testing.T) {
	kv := newAFMockKV()
	userID := uint64(10003)

	// Manually inject: user index points to factor id "missing-factor".
	missingFactorID := "missing-factor"
	_ = kv.Put(context.Background(), userIndexKey(userID, missingFactorID), []byte(missingFactorID))
	// Don't add primary record → Get(primaryKey(missingFactorID)) will return KeyNotFoundError.

	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	// List should skip missing factor (line 206-208: Get error → continue).
	factors, listErr := svc.List(context.Background(), userID)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(factors) != 0 {
		t.Fatalf("expected 0 factors, got %d", len(factors))
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – List: empty raw value (line 212-213)
// The user index lists a key, but kv.Get returns empty bytes.
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_List_EmptyFactorID(t *testing.T) {
	kv := newAFMockKV()
	userID := uint64(10004)

	// Store user index key with empty value.
	someKey := userIndexKey(userID, "some-factor")
	_ = kv.Put(context.Background(), someKey, []byte("")) // empty value

	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	// List skips the empty raw value (line 212-213: len(raw) == 0 → continue).
	factors, listErr := svc.List(context.Background(), userID)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(factors) != 0 {
		t.Fatalf("expected 0 factors (empty raw skipped), got %d", len(factors))
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Get: json.Unmarshal error (line 235-237)
// Store garbage JSON at the primary key.
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Get_UnmarshalError(t *testing.T) {
	kv := newAFMockKV()
	factorID := "bad-json-factor"
	_ = kv.Put(context.Background(), primaryKey(factorID), []byte("{invalid json"))

	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	_, getErr := svc.Get(context.Background(), factorID)
	if getErr == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – LookupByCredentialKey: lookupFactorID error
// (line 250-252) and Get error (line 257-259)
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_LookupByCredentialKey_IDFound_GetFails(t *testing.T) {
	kv := newAFMockKV()
	// Store a valid lookup key pointing to a factor that doesn't have primary data.
	lookupKey := lookupKeyFor(auth.AuthFactorMTLS, []byte("test-cred"))
	_ = kv.Put(context.Background(), lookupKey, []byte("orphan-factor-id"))
	// No primary record → Get will fail.

	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	_, lookupErr := svc.LookupByCredentialKey(context.Background(), auth.AuthFactorMTLS, []byte("test-cred"))
	if lookupErr == nil {
		t.Fatal("expected error when primary record is missing, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Rotate: json.Marshal error (line 357-359)
// This is virtually impossible since all fields are serializable.
// Skip and accept as unreachable.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Remove: countActive error (line 282-284)
// Inject a list error in the KV.
// ---------------------------------------------------------------------------

// countActive calls s.kv.List(ctx, userIndexPrefix(userID)).
// If List fails, countActive returns an error → Remove returns it at line 282-284.

type listErrorKV struct {
	*afMockKV
	failList bool
}

func (f *listErrorKV) List(ctx context.Context, prefix string) ([]string, error) {
	if f.failList {
		return nil, errors.New("list failed")
	}
	return f.afMockKV.List(ctx, prefix)
}

func TestUserAuthFactorService_Remove_CountActiveError(t *testing.T) {
	baseKV := newAFMockKV()
	lkv := &listErrorKV{afMockKV: baseKV, failList: false}

	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               lkv,
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	ctx := context.Background()
	userID := uint64(10010)

	// Add a factor (List works fine during Add).
	f := mtlsFactor("f-count-err", userID, []byte("cert-ce"))
	if addErr := svc.Add(ctx, userID, f); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	// Now fail List → countActive returns error → Remove returns error.
	lkv.failList = true
	err = svc.Remove(ctx, f.ID, false)
	if err == nil {
		t.Fatal("expected error from countActive list failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – Remove: SetUserActive error (line 301-303)
// force=true with active=1, SetUserActive fails.
// ---------------------------------------------------------------------------

type errorUserUpdater struct {
	err error
}

func (e *errorUserUpdater) SetUserActive(_ context.Context, _ uint64, _ bool) error {
	return e.err
}

func TestUserAuthFactorService_Remove_SetUserActiveFails(t *testing.T) {
	updater := &errorUserUpdater{err: errors.New("user update failed")}
	kv := newAFMockKV()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kv,
		UserStateUpdater: updater,
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	ctx := context.Background()
	userID := uint64(10020)

	// Add ONE factor.
	f := mtlsFactor("f-ua-fail", userID, []byte("cert-uaf"))
	if addErr := svc.Add(ctx, userID, f); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	// Remove with force=true — SetUserActive will fail.
	err = svc.Remove(ctx, f.ID, true)
	if err == nil {
		t.Fatal("expected error from SetUserActive failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – MarkUsed: KV Put error (line 416-418)
// and Relabel: KV Put error (line 426-428)
// After Add (3 puts), MarkUsed is the 4th Put.
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_MarkUsed_KVPutFails_V2(t *testing.T) {
	kv := newFailAfterNKV(3)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(10030)

	f := mtlsFactor("f-mu-fail", userID, []byte("cert-muf2"))
	if err := svc.Add(ctx, userID, f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	err := svc.MarkUsed(ctx, f.ID, time.Now())
	if err == nil {
		t.Fatal("expected error on MarkUsed KV put failure, got nil")
	}
}

func TestUserAuthFactorService_Relabel_KVPutFails_V2(t *testing.T) {
	kv := newFailAfterNKV(3)
	svc := makeAuthSvcWithKV(t, kv)
	ctx := context.Background()
	userID := uint64(10040)

	f := mtlsFactor("f-rl-fail", userID, []byte("cert-rlf2"))
	if err := svc.Add(ctx, userID, f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	err := svc.Relabel(ctx, f.ID, "new-label-2")
	if err == nil {
		t.Fatal("expected error on Relabel KV put failure, got nil")
	}
}

// errors is imported above.
