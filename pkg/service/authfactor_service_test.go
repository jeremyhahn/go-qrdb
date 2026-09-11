// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// In-memory KVServicer for authfactor service tests
// ---------------------------------------------------------------------------

type afMockKV struct {
	store map[string][]byte
}

var _ KVServicer = (*afMockKV)(nil)

func newAFMockKV() *afMockKV {
	return &afMockKV{store: make(map[string][]byte)}
}

func (m *afMockKV) Put(_ context.Context, key string, val []byte) error {
	m.store[key] = val
	return nil
}

// PutIfAbsent stores only when the key is absent, matching the real engines.
// The map lookup is the same check they perform; this mock is single-threaded,
// so it needs no further synchronisation.
func (m *afMockKV) PutIfAbsent(_ context.Context, key string, val []byte) (bool, error) {
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = val
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *afMockKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, key)
	return true, nil
}

func (m *afMockKV) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *afMockKV) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *afMockKV) List(_ context.Context, prefix string) ([]string, error) {
	keys := make([]string, 0)
	for k := range m.store {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *afMockKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *afMockKV) GetStale(_ context.Context, _ string) ([]byte, error)    { return nil, nil }
func (m *afMockKV) ListStale(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (m *afMockKV) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *afMockKV) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}
func (m *afMockKV) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}
func (m *afMockKV) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}
func (m *afMockKV) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}
func (m *afMockKV) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}
func (m *afMockKV) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}
func (m *afMockKV) PutEntity(_ context.Context, _ *PutEntityRequest) (*PutEntityResponse, error) {
	return &PutEntityResponse{}, nil
}
func (m *afMockKV) GetEntity(_ context.Context, _ *GetEntityRequest) (*GetEntityResponse, error) {
	return &GetEntityResponse{}, nil
}
func (m *afMockKV) DeleteEntity(_ context.Context, _ *DeleteEntityRequest) (*DeleteEntityResponse, error) {
	return &DeleteEntityResponse{}, nil
}
func (m *afMockKV) ListEntities(_ context.Context, _ *ListEntitiesRequest) (*ListEntitiesResponse, error) {
	return &ListEntitiesResponse{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newPIVAuthFactorService(t *testing.T) (AuthFactorServicer, *signingMockFactor) {
	t.Helper()
	f := newSigningMockFactor(t, "piv")
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      newAFMockKV(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	return svc, f
}

// ---------------------------------------------------------------------------
// NewAuthFactorService tests
// ---------------------------------------------------------------------------

func TestNewAuthFactorService_empty(t *testing.T) {
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		KV: newAFMockKV(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	if svc == nil {
		t.Fatal("NewAuthFactorService: returned nil service")
	}
}

func TestNewAuthFactorService_nilKV(t *testing.T) {
	// nil KV is allowed; Enroll/List will degrade gracefully.
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: nil,
		KV:      nil,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService(nil KV): %v", err)
	}
	if svc == nil {
		t.Fatal("NewAuthFactorService: returned nil service")
	}
}

// ---------------------------------------------------------------------------
// EnrollFactor tests
// ---------------------------------------------------------------------------

func TestAuthFactorService_EnrollFactor_success(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)

	enrollment, err := svc.EnrollFactor(context.Background(), "piv", "custodian_01", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if enrollment.EnrollmentID == "" {
		t.Error("EnrollFactor: EnrollmentID is empty")
	}
	if enrollment.FactorName != "piv" {
		t.Errorf("EnrollFactor: FactorName = %q, want \"piv\"", enrollment.FactorName)
	}
	if enrollment.UserID != "custodian_01" {
		t.Errorf("EnrollFactor: UserID = %q, want \"custodian_01\"", enrollment.UserID)
	}
}

func TestAuthFactorService_EnrollFactor_unknownFactor(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)

	_, err := svc.EnrollFactor(context.Background(), "unknown_factor", "u1", authfactors.EnrollParams{})
	if err == nil {
		t.Fatal("EnrollFactor(unknown): expected error, got nil")
	}
	var notFound *authfactors.FactorNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("EnrollFactor(unknown): expected FactorNotFoundError, got %T: %v", err, err)
	}
}

func TestAuthFactorService_EnrollFactor_persists(t *testing.T) {
	kv := newAFMockKV()
	f := newSigningMockFactor(t, "piv")
	svc, _ := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      kv,
	})

	enrollment, err := svc.EnrollFactor(context.Background(), "piv", "custodian_02", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	// Verify it was persisted.
	key := sysEnrollmentPrefix + enrollment.EnrollmentID
	data, err := kv.Get(context.Background(), key)
	if err != nil {
		t.Errorf("EnrollFactor: not persisted at %q: %v", key, err)
	}
	if len(data) == 0 {
		t.Error("EnrollFactor: persisted empty data")
	}
}

func TestAuthFactorService_EnrollFactor_nilKV_doesNotPanic(t *testing.T) {
	f := newSigningMockFactor(t, "piv")
	svc, _ := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      nil,
	})

	enrollment, err := svc.EnrollFactor(context.Background(), "piv", "custodian_03", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor(nil KV): %v", err)
	}
	if enrollment.EnrollmentID == "" {
		t.Error("EnrollFactor(nil KV): EnrollmentID is empty")
	}
}

// ---------------------------------------------------------------------------
// ChallengeFactor tests
// ---------------------------------------------------------------------------

func TestAuthFactorService_ChallengeFactor_success(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_04", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if challenge.ChallengeID == "" {
		t.Error("ChallengeFactor: ChallengeID is empty")
	}
	if len(challenge.Nonce) == 0 {
		t.Error("ChallengeFactor: Nonce is empty")
	}
	if challenge.Expired() {
		t.Error("ChallengeFactor: newly issued challenge must not be expired")
	}
}

func TestAuthFactorService_ChallengeFactor_notFound(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)

	_, err := svc.ChallengeFactor(context.Background(), "nonexistent-id")
	if err == nil {
		t.Fatal("ChallengeFactor(notfound): expected error, got nil")
	}
}

func TestAuthFactorService_ChallengeFactor_revoked(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_05", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if err := svc.RevokeFactor(ctx, enrollment.EnrollmentID); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}

	_, err = svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err == nil {
		t.Fatal("ChallengeFactor(revoked): expected error, got nil")
	}
	var revoked *authfactors.EnrollmentRevokedError
	if !errors.As(err, &revoked) {
		t.Errorf("ChallengeFactor(revoked): expected EnrollmentRevokedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// VerifyFactor tests
// ---------------------------------------------------------------------------

func TestAuthFactorService_VerifyFactor_success(t *testing.T) {
	f := newSigningMockFactor(t, "piv")
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      newAFMockKV(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	ctx := context.Background()
	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_06", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}

	slot := enrollment.Metadata["slot"]
	sig, err := f.SignNonce(ctx, slot, challenge.Nonce)
	if err != nil {
		t.Fatalf("SignNonce: %v", err)
	}

	response := &authfactors.Response{
		Value: sig,
		Metadata: map[string]string{
			"public_key_pem": string(enrollment.PublicKey),
		},
	}

	if err := svc.VerifyFactor(ctx, "piv", challenge.ChallengeID, response); err != nil {
		t.Errorf("VerifyFactor: %v", err)
	}
}

func TestAuthFactorService_VerifyFactor_challengeNotFound(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)

	_, err := svc.EnrollFactor(context.Background(), "piv", "custodian_07", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	err = svc.VerifyFactor(context.Background(), "piv", "nonexistent-challenge", &authfactors.Response{})
	if err == nil {
		t.Fatal("VerifyFactor(missing challenge): expected error, got nil")
	}
}

func TestAuthFactorService_VerifyFactor_badSignature(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_08", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}

	response := &authfactors.Response{
		Value:    []byte("invalid-signature"),
		Metadata: map[string]string{"public_key_pem": string(enrollment.PublicKey)},
	}

	err = svc.VerifyFactor(ctx, "piv", challenge.ChallengeID, response)
	if err == nil {
		t.Error("VerifyFactor(bad sig): expected error, got nil")
	}
}

func TestAuthFactorService_VerifyFactor_infersFactor(t *testing.T) {
	// Passing empty factorName: service should infer from enrollment.FactorName.
	f := newSigningMockFactor(t, "piv")
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      newAFMockKV(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	ctx := context.Background()
	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_09", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}

	slot := enrollment.Metadata["slot"]
	sig, err := f.SignNonce(ctx, slot, challenge.Nonce)
	if err != nil {
		t.Fatalf("SignNonce: %v", err)
	}

	response := &authfactors.Response{
		Value:    sig,
		Metadata: map[string]string{"public_key_pem": string(enrollment.PublicKey)},
	}

	// Empty factorName — should infer "piv" from the enrollment record.
	if err := svc.VerifyFactor(ctx, "", challenge.ChallengeID, response); err != nil {
		t.Errorf("VerifyFactor(empty factorName): %v", err)
	}
}

func TestAuthFactorService_VerifyFactor_preventReplay(t *testing.T) {
	f := newSigningMockFactor(t, "piv")
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      newAFMockKV(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}

	ctx := context.Background()
	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_10", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}

	slot := enrollment.Metadata["slot"]
	sig, err := f.SignNonce(ctx, slot, challenge.Nonce)
	if err != nil {
		t.Fatalf("SignNonce: %v", err)
	}

	response := &authfactors.Response{
		Value:    sig,
		Metadata: map[string]string{"public_key_pem": string(enrollment.PublicKey)},
	}

	// First verify must succeed.
	if err := svc.VerifyFactor(ctx, "piv", challenge.ChallengeID, response); err != nil {
		t.Fatalf("VerifyFactor #1: %v", err)
	}

	// Second verify with the same challenge ID must fail (replay prevention).
	err = svc.VerifyFactor(ctx, "piv", challenge.ChallengeID, response)
	if err == nil {
		t.Error("VerifyFactor(replay): expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListFactors tests
// ---------------------------------------------------------------------------

func TestAuthFactorService_ListFactors_empty(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)

	list, err := svc.ListFactors(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	// No enrollments yet; list must be empty (not nil).
	if list == nil {
		list = []*authfactors.Enrollment{}
	}
	if len(list) != 0 {
		t.Errorf("ListFactors(empty): expected 0, got %d", len(list))
	}
}

func TestAuthFactorService_ListFactors_allSubjects(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	for _, uid := range []string{"user_a", "user_b", "user_c"} {
		if _, err := svc.EnrollFactor(ctx, "piv", uid, authfactors.EnrollParams{}); err != nil {
			t.Fatalf("EnrollFactor(%s): %v", uid, err)
		}
	}

	list, err := svc.ListFactors(ctx, "")
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("ListFactors(all): expected 3, got %d", len(list))
	}
}

func TestAuthFactorService_ListFactors_filterBySubject(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	if _, err := svc.EnrollFactor(ctx, "piv", "user_x", authfactors.EnrollParams{}); err != nil {
		t.Fatalf("EnrollFactor(user_x): %v", err)
	}
	if _, err := svc.EnrollFactor(ctx, "piv", "user_y", authfactors.EnrollParams{}); err != nil {
		t.Fatalf("EnrollFactor(user_y): %v", err)
	}

	list, err := svc.ListFactors(ctx, "user_x")
	if err != nil {
		t.Fatalf("ListFactors(user_x): %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListFactors(user_x): expected 1, got %d", len(list))
	}
	if list[0].UserID != "user_x" {
		t.Errorf("ListFactors(user_x): UserID = %q, want \"user_x\"", list[0].UserID)
	}
}

func TestAuthFactorService_ListFactors_nilKV(t *testing.T) {
	f := newSigningMockFactor(t, "piv")
	svc, _ := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{f},
		KV:      nil,
	})

	list, err := svc.ListFactors(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFactors(nil KV): %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListFactors(nil KV): expected empty list, got %d entries", len(list))
	}
}

// ---------------------------------------------------------------------------
// RevokeFactor tests
// ---------------------------------------------------------------------------

func TestAuthFactorService_RevokeFactor_success(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_11", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	if err := svc.RevokeFactor(ctx, enrollment.EnrollmentID); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
}

func TestAuthFactorService_RevokeFactor_notFound(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)

	err := svc.RevokeFactor(context.Background(), "nonexistent-id")
	if err == nil {
		t.Fatal("RevokeFactor(notfound): expected error, got nil")
	}
}

func TestAuthFactorService_RevokeFactor_alreadyRevoked(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_12", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	if err := svc.RevokeFactor(ctx, enrollment.EnrollmentID); err != nil {
		t.Fatalf("RevokeFactor #1: %v", err)
	}

	// Second revocation must fail with EnrollmentRevokedError.
	err = svc.RevokeFactor(ctx, enrollment.EnrollmentID)
	if err == nil {
		t.Fatal("RevokeFactor(already revoked): expected error, got nil")
	}
	var revoked *authfactors.EnrollmentRevokedError
	if !errors.As(err, &revoked) {
		t.Errorf("RevokeFactor(already revoked): expected EnrollmentRevokedError, got %T: %v", err, err)
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*afMockKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *afMockKV) ListAll(a0 context.Context, prefix string) ([]string, error) {
	return m.List(a0, prefix)
}
