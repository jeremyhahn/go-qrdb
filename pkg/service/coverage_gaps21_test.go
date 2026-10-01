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

// coverage_gaps21_test.go covers the final 6 statements for 91%+.
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
)

// ---------------------------------------------------------------------------
// authfactor_service.go – ListFactors: kv.Get error for enrollment key (line 189)
// ---------------------------------------------------------------------------

// listAndGetErrorKV - returns a key list but fails on Get for that key.
type listAndGetErrorKV struct {
	*afMockKV
	getErr    error
	failOnKey string
}

func (f *listAndGetErrorKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failOnKey != "" && key == f.failOnKey {
		return nil, f.getErr
	}
	return f.afMockKV.Get(ctx, key)
}

func TestAuthFactorService_ListFactors_GetErrorForKey(t *testing.T) {
	kv := newAFMockKV()
	enrollmentID := "get-error-enrollment"
	fullKey := sysEnrollmentPrefix + enrollmentID
	// Store some bytes so List returns the key.
	_ = kv.Put(context.Background(), fullKey, []byte("some-val"))

	// Wrap with a Get-failing KV.
	lgkv := &listAndGetErrorKV{
		afMockKV:  kv,
		getErr:    errors.New("get failed"),
		failOnKey: fullKey,
	}

	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{},
		KV:      lgkv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	// ListFactors: List succeeds (returns fullKey), Get(fullKey) fails with an
	// error that is NOT KeyNotFoundError. Per go-qrdb#464 review round 3, this
	// must ABORT the call rather than silently omit the record: a caller must
	// never be told a shorter-than-true list is complete.
	enrollments, listErr := svc.ListFactors(context.Background(), "")
	if enrollments != nil {
		t.Fatalf("expected no partial result, got %d enrollments", len(enrollments))
	}
	var readErr *AuthFactorListReadError
	if !errors.As(listErr, &readErr) {
		t.Fatalf("ListFactors: want *AuthFactorListReadError, got %T: %v", listErr, listErr)
	}
	if readErr.Key != fullKey {
		t.Fatalf("AuthFactorListReadError.Key = %q, want %q", readErr.Key, fullKey)
	}
	if !errors.Is(listErr, lgkv.getErr) {
		t.Fatalf("ListFactors error does not unwrap to the underlying Get failure: %v", listErr)
	}
}

// TestAuthFactorService_ChallengeFactor_PropagatesOperationalGetError covers
// the other half of the same review finding: loadEnrollment used to fold
// EVERY Get error into EnrollmentNotFoundError, including one that has
// nothing to do with the record being absent — a sealed tenant barrier, a
// Raft read failure, anything the store itself reports as broken rather than
// empty. Reporting that as "not found" tells the caller to stop looking
// instead of to retry or report the outage, which is the wrong answer for an
// operational failure. This pins that a non-KeyNotFoundError Get failure
// reaches ChallengeFactor's caller unfolded.
func TestAuthFactorService_ChallengeFactor_PropagatesOperationalGetError(t *testing.T) {
	kv := newAFMockKV()
	enrollmentID := "sealed-mid-request"
	fullKey := sysEnrollmentPrefix + enrollmentID
	_ = kv.Put(context.Background(), fullKey, []byte("some-val"))

	opErr := errors.New("tenant barrier sealed")
	lgkv := &listAndGetErrorKV{afMockKV: kv, getErr: opErr, failOnKey: fullKey}

	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{},
		KV:      lgkv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	_, challengeErr := svc.ChallengeFactor(context.Background(), enrollmentID)
	var notFound *authfactors.EnrollmentNotFoundError
	if errors.As(challengeErr, &notFound) {
		t.Fatalf("ChallengeFactor folded an operational error into EnrollmentNotFoundError: %v", challengeErr)
	}
	if !errors.Is(challengeErr, opErr) {
		t.Fatalf("ChallengeFactor error does not unwrap to the underlying Get failure: %v", challengeErr)
	}
}

// TestAuthFactorService_VerifyFactor_PropagatesOperationalChallengeGetError
// is the same finding applied to consumeChallenge's Get, reached from
// VerifyFactor: an operational failure reading the pending challenge must
// not be reported as "no such challenge".
func TestAuthFactorService_VerifyFactor_PropagatesOperationalChallengeGetError(t *testing.T) {
	kv := newAFMockKV()
	challengeID := challengePublicID(1, "sealed-mid-verify")
	fullKey, _ := parseChallengeID(challengeID)
	_ = kv.Put(context.Background(), fullKey, []byte("some-val"))

	opErr := errors.New("tenant barrier sealed")
	lgkv := &listAndGetErrorKV{afMockKV: kv, getErr: opErr, failOnKey: fullKey}

	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{},
		KV:      lgkv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	verifyErr := svc.VerifyFactor(context.Background(), "", challengeID, &authfactors.Response{})
	var notFound *authfactors.EnrollmentNotFoundError
	if errors.As(verifyErr, &notFound) {
		t.Fatalf("VerifyFactor folded an operational error into EnrollmentNotFoundError: %v", verifyErr)
	}
	if !errors.Is(verifyErr, opErr) {
		t.Fatalf("VerifyFactor error does not unwrap to the underlying Get failure: %v", verifyErr)
	}
}

// ---------------------------------------------------------------------------
// authfactor_service.go – loadEnrollment: kv.Get returns empty (line 236-238)
// and unmarshal error (line 244-246).
// ---------------------------------------------------------------------------

func TestAuthFactorService_LoadEnrollment_GetReturnsEmpty(t *testing.T) {
	// Empty bytes stored at the enrollment key.
	kv := newAFMockKV()
	enrollmentID := "empty-load-id"
	_ = kv.Put(context.Background(), sysEnrollmentPrefix+enrollmentID, []byte(""))

	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{},
		KV:      kv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	// ChallengeFactor → loadEnrollment → kv.Get returns empty → err path (line 236-238).
	_, challengeErr := svc.ChallengeFactor(context.Background(), enrollmentID)
	if challengeErr == nil {
		t.Fatal("expected error for empty enrollment data, got nil")
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – ExistsWithConsistency: KeyNotFoundError path (line 438)
// The existing KVService test covers the service layer ExistsWithConsistency.
// The MVCCKVService.ExistsWithConsistency checks keyNotFoundErr (not *KeyNotFoundError).
// ---------------------------------------------------------------------------

func TestMVCCKVService_ExistsWithConsistency_KeyNotFound_Mock(t *testing.T) {
	// keyNotFoundErr interface: has KeyNotFound() string method.
	// Use mockKeyNotFoundError from mvcc_service_test.go.
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "k"}
		},
	}
	svc := NewMVCCKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for KeyNotFoundError")
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – GetRevisionWithConsistency: error branch (line 527-529)
// Already covered? Let's verify.
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetRevisionWithConsistency_Error_v2(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("consistency read error")
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetRevisionWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – GetAtRevisionWithConsistency: error branch (line 567-569)
// Already covered? Let's verify.
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetAtRevisionWithConsistency_Error_v2(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("at-revision read error")
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetAtRevisionWithConsistency(context.Background(), "k", 1, consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – RangeWithConsistency: error branch (line 612-614, 623-625)
// Already covered? Let's verify.
// ---------------------------------------------------------------------------

func TestMVCCKVService_RangeWithConsistency_Error_v2(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("range consistency error")
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, _, _, err := svc.RangeWithConsistency(context.Background(), []byte("a"), []byte("z"), consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// collection_service.go – CreateCollection: codec validation path (186-188, 190-192)
// Line 186-188: invalid codec error return
// Line 190-192: collectionMetadataKey Get error check
// ---------------------------------------------------------------------------

func TestCollectionService_CreateCollection_InvalidCodec_v2(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "bad-codec-col-v2",
		Database:     "mydb",
		StorageCodec: "msgpack-not-available",
	})
	if err == nil {
		t.Fatal("expected error for invalid codec, got nil")
	}
}
