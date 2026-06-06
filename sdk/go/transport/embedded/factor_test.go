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

package embedded

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// In-memory user auth factor service
// ---------------------------------------------------------------------------

// memUserAuthFactorService is a tiny in-memory implementation of
// service.UserAuthFactorServicer used to drive the FactorClient tests
// without the full KV backend.
type memUserAuthFactorService struct {
	mu      sync.Mutex
	factors map[string]*auth.AuthFactor
}

func newMemUserAuthFactorService() *memUserAuthFactorService {
	return &memUserAuthFactorService{factors: make(map[string]*auth.AuthFactor)}
}

func (m *memUserAuthFactorService) Add(_ context.Context, userID uint64, factor *auth.AuthFactor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if userID == 0 {
		return &auth.InvalidFactorError{Reason: "userID zero"}
	}
	if factor == nil {
		return &auth.InvalidFactorError{Reason: "factor nil"}
	}
	if !factor.Type.IsValid() {
		return &auth.FactorTypeUnsupportedError{Type: string(factor.Type)}
	}
	if len(factor.Credential) == 0 {
		return &auth.InvalidFactorError{Reason: "empty credential"}
	}
	for _, existing := range m.factors {
		if existing.Type == factor.Type && string(existing.Credential) == string(factor.Credential) {
			return &auth.FactorAlreadyExistsError{Type: factor.Type, FactorID: existing.ID}
		}
	}
	factor.UserID = userID
	if factor.ID == "" {
		factor.ID = uuid.NewString()
	}
	if factor.RegisteredAt.IsZero() {
		factor.RegisteredAt = time.Now().UTC()
	}
	m.factors[factor.ID] = factor.Clone()
	return nil
}

func (m *memUserAuthFactorService) List(_ context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*auth.AuthFactor, 0, len(m.factors))
	for _, f := range m.factors {
		if f.UserID == userID {
			out = append(out, f.Clone())
		}
	}
	return out, nil
}

func (m *memUserAuthFactorService) Get(_ context.Context, factorID string) (*auth.AuthFactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.factors[factorID]
	if !ok {
		return nil, &auth.FactorNotFoundError{FactorID: factorID}
	}
	return f.Clone(), nil
}

func (m *memUserAuthFactorService) LookupByCredentialKey(_ context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.factors {
		if f.Type == t && string(f.Credential) == string(key) {
			if f.RevokedAt != nil {
				return nil, &auth.FactorRevokedError{FactorID: f.ID}
			}
			return f.Clone(), nil
		}
	}
	return nil, &auth.FactorNotFoundError{}
}

func (m *memUserAuthFactorService) Remove(_ context.Context, factorID string, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.factors[factorID]
	if !ok {
		return &auth.FactorNotFoundError{FactorID: factorID}
	}
	now := time.Now().UTC()
	f.RevokedAt = &now
	return nil
}

func (m *memUserAuthFactorService) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return errors.New("not implemented")
}

func (m *memUserAuthFactorService) Relabel(_ context.Context, _ string, _ string) error {
	return errors.New("not implemented")
}

func (m *memUserAuthFactorService) MarkUsed(_ context.Context, factorID string, ts time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.factors[factorID]
	if !ok {
		return &auth.FactorNotFoundError{FactorID: factorID}
	}
	t := ts
	f.LastUsedAt = &t
	return nil
}

// ---------------------------------------------------------------------------
// Mock FactorVerifier
// ---------------------------------------------------------------------------

type mockVerifier struct {
	typ     transport.FactorType
	verifyF func(ctx context.Context, factor *auth.AuthFactor, challenge transport.VerifyChallenge) error
}

func (v *mockVerifier) Type() transport.FactorType { return v.typ }
func (v *mockVerifier) Verify(ctx context.Context, factor *auth.AuthFactor, challenge transport.VerifyChallenge) error {
	if v.verifyF != nil {
		return v.verifyF(ctx, factor, challenge)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newFactorTransport(t *testing.T, opts ...Option) *Transport {
	t.Helper()
	defaults := []Option{
		WithUserAuthFactorService(newMemUserAuthFactorService()),
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, append(defaults, opts...)...)
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

// ---------------------------------------------------------------------------
// Enroll
// ---------------------------------------------------------------------------

func TestFactorClient_Enroll_Happy(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type:       transport.FactorTypeFIDO2,
		Label:      "yubikey",
		Credential: []byte("cred-1"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := fc.List(context.Background(), 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 factor, got %d", len(got))
	}
	if got[0].Label != "yubikey" {
		t.Errorf("expected label yubikey, got %q", got[0].Label)
	}
}

func TestFactorClient_Enroll_InvalidType(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type:       transport.FactorType("bogus"),
		Credential: []byte("x"),
	})
	if err == nil {
		t.Fatal("expected FactorTypeUnsupportedError, got nil")
	}
	var typeErr *transport.FactorTypeUnsupportedError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected *transport.FactorTypeUnsupportedError, got %T", err)
	}
}

func TestFactorClient_Enroll_InvalidUserID(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	err := fc.Enroll(context.Background(), 0, transport.Factor{
		Type:       transport.FactorTypeFIDO2,
		Credential: []byte("c"),
	})
	if err == nil {
		t.Fatal("expected error for zero user id, got nil")
	}
	var nf *transport.FactorNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.FactorNotFoundError, got %T", err)
	}
}

func TestFactorClient_Enroll_DuplicateCredential(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	f := transport.Factor{
		Type:       transport.FactorTypePIV,
		Credential: []byte("cred-1"),
	}
	if err := fc.Enroll(context.Background(), 1, f); err != nil {
		t.Fatalf("first enroll: %v", err)
	}

	err := fc.Enroll(context.Background(), 1, f)
	if err == nil {
		t.Fatal("expected FactorAlreadyEnrolledError, got nil")
	}
	var dup *transport.FactorAlreadyEnrolledError
	if !errors.As(err, &dup) {
		t.Errorf("expected *transport.FactorAlreadyEnrolledError, got %T", err)
	}
}

func TestFactorClient_Enroll_MissingService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fc := tr.FactorClient()

	err = fc.Enroll(context.Background(), 1, transport.Factor{
		Type:       transport.FactorTypeFIDO2,
		Credential: []byte("c"),
	})
	if err == nil {
		t.Fatal("expected MissingUserAuthFactorServiceError, got nil")
	}
	var miss *MissingUserAuthFactorServiceError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingUserAuthFactorServiceError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestFactorClient_List_AfterEnroll(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	for _, cred := range []string{"a", "b", "c"} {
		if err := fc.Enroll(context.Background(), 1, transport.Factor{
			Type:       transport.FactorTypeFIDO2,
			Credential: []byte(cred),
		}); err != nil {
			t.Fatalf("enroll %s: %v", cred, err)
		}
	}

	got, err := fc.List(context.Background(), 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 factors, got %d", len(got))
	}
}

func TestFactorClient_List_InvalidUserID(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	_, err := fc.List(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error for zero user id, got nil")
	}
	var nf *transport.FactorNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.FactorNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

func TestFactorClient_Remove_HidesFromList(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	// enroll two factors so removing one isn't a "last factor" guard.
	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("a"),
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("b"),
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	listed, err := fc.List(context.Background(), 1)
	if err != nil || len(listed) != 2 {
		t.Fatalf("expected 2, got %d (err=%v)", len(listed), err)
	}
	target := listed[0].ID

	if err := fc.Remove(context.Background(), 1, target); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	after, _ := fc.List(context.Background(), 1)
	for _, f := range after {
		if f.ID == target && f.IsActive() {
			t.Errorf("expected factor %s revoked after remove", target)
		}
	}
}

func TestFactorClient_Remove_NotFound(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	err := fc.Remove(context.Background(), 1, "missing")
	if err == nil {
		t.Fatal("expected FactorNotFoundError, got nil")
	}
	var nf *transport.FactorNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.FactorNotFoundError, got %T", err)
	}
}

func TestFactorClient_Remove_EmptyFactorID(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	err := fc.Remove(context.Background(), 1, "")
	if err == nil {
		t.Fatal("expected FactorNotFoundError for empty id, got nil")
	}
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

func TestFactorClient_Verify_Happy(t *testing.T) {
	verifier := &mockVerifier{
		typ: transport.FactorTypeFIDO2,
		verifyF: func(_ context.Context, _ *auth.AuthFactor, _ transport.VerifyChallenge) error {
			return nil
		},
	}
	tr := newFactorTransport(t, WithFactorVerifier(verifier))
	fc := tr.FactorClient()

	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("c"),
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	listed, _ := fc.List(context.Background(), 1)
	target := listed[0].ID

	got, err := fc.Verify(context.Background(), 1, transport.VerifyChallenge{
		FactorID:  target,
		Type:      transport.FactorTypeFIDO2,
		Nonce:     []byte("n"),
		Signature: []byte("s"),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.FactorID != target {
		t.Errorf("expected FactorID %q, got %q", target, got.FactorID)
	}
	if got.UserID != 1 {
		t.Errorf("expected UserID 1, got %d", got.UserID)
	}
}

func TestFactorClient_Verify_VerifierRejects(t *testing.T) {
	verifier := &mockVerifier{
		typ: transport.FactorTypeFIDO2,
		verifyF: func(_ context.Context, _ *auth.AuthFactor, _ transport.VerifyChallenge) error {
			return errors.New("bad signature")
		},
	}
	tr := newFactorTransport(t, WithFactorVerifier(verifier))
	fc := tr.FactorClient()

	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("c"),
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	listed, _ := fc.List(context.Background(), 1)
	target := listed[0].ID

	_, err := fc.Verify(context.Background(), 1, transport.VerifyChallenge{
		FactorID:  target,
		Type:      transport.FactorTypeFIDO2,
		Nonce:     []byte("n"),
		Signature: []byte("s"),
	})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	var ve *transport.FactorVerificationError
	if !errors.As(err, &ve) {
		t.Errorf("expected *transport.FactorVerificationError, got %T", err)
	}
}

func TestFactorClient_Verify_NoVerifier(t *testing.T) {
	tr := newFactorTransport(t)
	fc := tr.FactorClient()

	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("c"),
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	listed, _ := fc.List(context.Background(), 1)
	target := listed[0].ID

	_, err := fc.Verify(context.Background(), 1, transport.VerifyChallenge{
		FactorID:  target,
		Type:      transport.FactorTypeFIDO2,
		Nonce:     []byte("n"),
		Signature: []byte("s"),
	})
	if err == nil {
		t.Fatal("expected FactorTypeUnsupportedError, got nil")
	}
	var typeErr *transport.FactorTypeUnsupportedError
	if !errors.As(err, &typeErr) {
		t.Errorf("expected *transport.FactorTypeUnsupportedError, got %T", err)
	}
}

func TestFactorClient_Verify_RevokedFactor(t *testing.T) {
	verifier := &mockVerifier{typ: transport.FactorTypeFIDO2}
	tr := newFactorTransport(t, WithFactorVerifier(verifier))
	fc := tr.FactorClient()

	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("a"),
	}); err != nil {
		t.Fatalf("enroll a: %v", err)
	}
	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("b"),
	}); err != nil {
		t.Fatalf("enroll b: %v", err)
	}
	listed, _ := fc.List(context.Background(), 1)
	target := listed[0].ID
	if err := fc.Remove(context.Background(), 1, target); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, err := fc.Verify(context.Background(), 1, transport.VerifyChallenge{
		FactorID:  target,
		Type:      transport.FactorTypeFIDO2,
		Nonce:     []byte("n"),
		Signature: []byte("s"),
	})
	if err == nil {
		t.Fatal("expected verification failure for revoked factor")
	}
	var ve *transport.FactorVerificationError
	if !errors.As(err, &ve) {
		t.Errorf("expected *transport.FactorVerificationError, got %T", err)
	}
}

func TestFactorClient_Verify_CrossUserDenied(t *testing.T) {
	verifier := &mockVerifier{typ: transport.FactorTypeFIDO2}
	tr := newFactorTransport(t, WithFactorVerifier(verifier))
	fc := tr.FactorClient()

	if err := fc.Enroll(context.Background(), 1, transport.Factor{
		Type: transport.FactorTypeFIDO2, Credential: []byte("c"),
	}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	listed, _ := fc.List(context.Background(), 1)
	target := listed[0].ID

	// Verify under user "2" — must be rejected as not-found.
	_, err := fc.Verify(context.Background(), 2, transport.VerifyChallenge{
		FactorID:  target,
		Type:      transport.FactorTypeFIDO2,
		Nonce:     []byte("n"),
		Signature: []byte("s"),
	})
	if err == nil {
		t.Fatal("expected cross-user verify to be rejected")
	}
	var nf *transport.FactorNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.FactorNotFoundError, got %T", err)
	}
}
