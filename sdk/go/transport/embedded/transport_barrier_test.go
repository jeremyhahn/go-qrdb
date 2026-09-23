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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock BarrierServicer
// ---------------------------------------------------------------------------

// mockBarrierService implements service.BarrierServicer with function-field
// injection so each test can control the behavior of every method.
type mockBarrierService struct {
	barrierInitializeFn         func(ctx context.Context, secret []byte) error
	barrierUnsealFn             func(ctx context.Context, secret []byte) error
	barrierSealFn               func(ctx context.Context) error
	barrierStatusFn             func(ctx context.Context) (*service.BarrierStatusInfo, error)
	barrierInitializeShamirFn   func(ctx context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error)
	barrierUnsealWithShareFn    func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	barrierUnsealWithSharesFn   func(ctx context.Context, shares []string) error
	barrierRekeyFn              func(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error)
	barrierGenRecoveryKeysFn    func(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error)
	barrierRecoverWithKeysFn    func(ctx context.Context, keys []string) error
	barrierDeleteRecoveryKeysFn func(ctx context.Context) error
	barrierHasRecoveryKeysFn    func(ctx context.Context) (bool, error)
	barrierGenRootTokenFn       func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)
}

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.barrierInitializeFn != nil {
		return m.barrierInitializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx)
	}
	return &service.BarrierStatusInfo{}, nil
}

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error) {
	if m.barrierInitializeShamirFn != nil {
		return m.barrierInitializeShamirFn(ctx, secret, threshold, totalShares)
	}
	return &service.ShamirInitInfo{}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShare(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.barrierUnsealWithShareFn != nil {
		return m.barrierUnsealWithShareFn(ctx, share)
	}
	return &service.UnsealShareProgress{}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShares(ctx context.Context, shares []string) error {
	if m.barrierUnsealWithSharesFn != nil {
		return m.barrierUnsealWithSharesFn(ctx, shares)
	}
	return nil
}

func (m *mockBarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.barrierRekeyFn != nil {
		return m.barrierRekeyFn(ctx, threshold, total)
	}
	return &service.ShamirInitInfo{}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	if m.barrierGenRecoveryKeysFn != nil {
		return m.barrierGenRecoveryKeysFn(ctx, threshold, total)
	}
	return &service.RecoveryKeysInfo{}, nil
}

func (m *mockBarrierService) BarrierRecoverWithKeys(ctx context.Context, keys []string) error {
	if m.barrierRecoverWithKeysFn != nil {
		return m.barrierRecoverWithKeysFn(ctx, keys)
	}
	return nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.barrierDeleteRecoveryKeysFn != nil {
		return m.barrierDeleteRecoveryKeysFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.barrierHasRecoveryKeysFn != nil {
		return m.barrierHasRecoveryKeysFn(ctx)
	}
	return false, nil
}

func (m *mockBarrierService) BarrierGenerateRootToken(ctx context.Context, shares []string) (*service.RootTokenInfo, error) {
	if m.barrierGenRootTokenFn != nil {
		return m.barrierGenRootTokenFn(ctx, shares)
	}
	return &service.RootTokenInfo{}, nil
}

func (m *mockBarrierService) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}

func (m *mockBarrierService) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}

func (m *mockBarrierService) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}

func (m *mockBarrierService) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}

func (m *mockBarrierService) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}

func (m *mockBarrierService) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{}, nil
}

func (m *mockBarrierService) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}

func (m *mockBarrierService) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}

func (m *mockBarrierService) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}

// newBarrierTransport creates a transport with the given barrier service
// injected via the WithBarrierService option.
func newBarrierTransport(t *testing.T, svc service.BarrierServicer) *Transport {
	t.Helper()
	opts := []Option{}
	if svc != nil {
		opts = append(opts, WithBarrierService(svc))
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, opts...)
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

// closedBarrierTransport creates a transport with a barrier service that has
// already been closed.
func closedBarrierTransport(t *testing.T) *Transport {
	t.Helper()
	tr := newBarrierTransport(t, &mockBarrierService{})
	if err := tr.Close(); err != nil {
		t.Fatalf("failed to close transport: %v", err)
	}
	return tr
}

// errBarrier is a sentinel error used by mock barrier methods.
var errBarrier = errors.New("barrier: test error")

// ---------------------------------------------------------------------------
// BarrierInitialize
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_Initialize(t *testing.T) {
	var capturedSecret string
	svc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, secret []byte) error {
			capturedSecret = string(secret)
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: []byte("my-master-key"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if capturedSecret != "my-master-key" {
		t.Errorf("expected secret 'my-master-key', got %q", capturedSecret)
	}
}

func TestEmbeddedBarrier_Initialize_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: []byte("secret"),
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Initialize_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: []byte("secret"),
	})
	if err == nil {
		t.Fatal("expected error for nil barrier service")
	}
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
	if nilErr.Service != "BarrierServicer" {
		t.Errorf("expected service name 'BarrierServicer', got %q", nilErr.Service)
	}
}

func TestEmbeddedBarrier_Initialize_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, _ []byte) error {
			return errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: []byte("secret"),
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierUnseal
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_Unseal(t *testing.T) {
	var capturedSecret string
	svc := &mockBarrierService{
		barrierUnsealFn: func(_ context.Context, secret []byte) error {
			capturedSecret = string(secret)
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{
		Secret: []byte("unseal-key"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if capturedSecret != "unseal-key" {
		t.Errorf("expected secret 'unseal-key', got %q", capturedSecret)
	}
}

func TestEmbeddedBarrier_Unseal_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{
		Secret: []byte("secret"),
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Unseal_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{
		Secret: []byte("secret"),
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BarrierSeal
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_Seal(t *testing.T) {
	sealCalled := false
	svc := &mockBarrierService{
		barrierSealFn: func(_ context.Context) error {
			sealCalled = true
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.BarrierSeal(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !sealCalled {
		t.Error("expected BarrierSeal to be called on service")
	}
}

func TestEmbeddedBarrier_Seal_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierSeal(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Seal_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierSeal(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BarrierStatus
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_Status(t *testing.T) {
	svc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:         true,
				Strategy:       "shamir",
				HardwareBacked: false,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !resp.Sealed {
		t.Error("expected sealed=true")
	}
	if resp.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", resp.Strategy)
	}
	if resp.HardwareBacked {
		t.Error("expected hardware_backed=false")
	}
	if resp.InitializedAt != "" {
		t.Errorf("expected empty initialized_at for zero time, got %q", resp.InitializedAt)
	}
}

func TestEmbeddedBarrier_Status_InitializedAt(t *testing.T) {
	initTime := time.Date(2026, 3, 4, 12, 30, 0, 0, time.UTC)
	svc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:         false,
				Strategy:       "passphrase",
				HardwareBacked: false,
				InitializedAt:  initTime,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	expected := initTime.Format(time.RFC3339)
	if resp.InitializedAt != expected {
		t.Errorf("expected initialized_at %q, got %q", expected, resp.InitializedAt)
	}
	if resp.Sealed {
		t.Error("expected sealed=false")
	}
}

func TestEmbeddedBarrier_Status_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierStatus(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Status_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierStatus(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Status_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierStatus(context.Background())
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierInitializeShamir
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_ShamirInitialize(t *testing.T) {
	svc := &mockBarrierService{
		barrierInitializeShamirFn: func(_ context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error) {
			if string(secret) != "shamir-secret" {
				t.Errorf("expected secret 'shamir-secret', got %q", secret)
			}
			if threshold != 3 {
				t.Errorf("expected threshold 3, got %d", threshold)
			}
			if totalShares != 5 {
				t.Errorf("expected totalShares 5, got %d", totalShares)
			}
			return &service.ShamirInitInfo{
				ShareCount:  5,
				Threshold:   3,
				TotalShares: 5,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret:      []byte("shamir-secret"),
		Threshold:   3,
		TotalShares: 5,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.ShareCount != 5 {
		t.Errorf("expected ShareCount=5, got %d", resp.ShareCount)
	}
	if resp.Threshold != 3 {
		t.Errorf("expected threshold 3, got %d", resp.Threshold)
	}
	if resp.TotalShares != 5 {
		t.Errorf("expected total_shares 5, got %d", resp.TotalShares)
	}
}

func TestEmbeddedBarrier_ShamirInitialize_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret:      []byte("s"),
		Threshold:   2,
		TotalShares: 3,
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ShamirInitialize_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret:      []byte("s"),
		Threshold:   2,
		TotalShares: 3,
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ShamirInitialize_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierInitializeShamirFn: func(_ context.Context, _ []byte, _, _ int) (*service.ShamirInitInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret:      []byte("s"),
		Threshold:   2,
		TotalShares: 3,
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierUnsealWithShare
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_ShamirUnsealShare(t *testing.T) {
	svc := &mockBarrierService{
		barrierUnsealWithShareFn: func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
			if share != "share-1" {
				t.Errorf("expected share 'share-1', got %q", share)
			}
			return &service.UnsealShareProgress{
				Required:  3,
				Submitted: 1,
				Complete:  false,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{
		Share: "share-1",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.Required != 3 {
		t.Errorf("expected required=3, got %d", resp.Required)
	}
	if resp.Submitted != 1 {
		t.Errorf("expected submitted=1, got %d", resp.Submitted)
	}
	if resp.Complete {
		t.Error("expected complete=false")
	}
}

func TestEmbeddedBarrier_ShamirUnsealShare_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{
		Share: "share-1",
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ShamirUnsealShare_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{
		Share: "share-1",
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ShamirUnsealShare_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierUnsealWithShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{
		Share: "invalid-share",
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierUnsealWithShares
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_ShamirUnsealShares(t *testing.T) {
	var capturedShares []string
	svc := &mockBarrierService{
		barrierUnsealWithSharesFn: func(_ context.Context, shares []string) error {
			capturedShares = shares
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	shares := []string{"share-1", "share-2", "share-3"}
	err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{
		Shares: shares,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(capturedShares) != 3 {
		t.Errorf("expected 3 shares, got %d", len(capturedShares))
	}
	for i, s := range shares {
		if capturedShares[i] != s {
			t.Errorf("share[%d]: expected %q, got %q", i, s, capturedShares[i])
		}
	}
}

func TestEmbeddedBarrier_ShamirUnsealShares_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{
		Shares: []string{"s1"},
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ShamirUnsealShares_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{
		Shares: []string{"s1"},
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BarrierRekey
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_Rekey(t *testing.T) {
	svc := &mockBarrierService{
		barrierRekeyFn: func(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
			if threshold != 2 {
				t.Errorf("expected threshold=2, got %d", threshold)
			}
			if total != 5 {
				t.Errorf("expected total=5, got %d", total)
			}
			return &service.ShamirInitInfo{
				ShareCount:  5,
				Threshold:   2,
				TotalShares: 5,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2,
		Total:     5,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.ShareCount != 5 {
		t.Errorf("expected ShareCount=5, got %d", resp.ShareCount)
	}
	if resp.Threshold != 2 {
		t.Errorf("expected threshold=2, got %d", resp.Threshold)
	}
	if resp.TotalShares != 5 {
		t.Errorf("expected total_shares=5, got %d", resp.TotalShares)
	}
}

func TestEmbeddedBarrier_Rekey_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2,
		Total:     3,
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Rekey_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2,
		Total:     3,
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Rekey_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierRekeyFn: func(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2,
		Total:     3,
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierGenerateRecoveryKeys
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_RecoveryGenerate(t *testing.T) {
	svc := &mockBarrierService{
		barrierGenRecoveryKeysFn: func(_ context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
			if threshold != 2 {
				t.Errorf("expected threshold=2, got %d", threshold)
			}
			if total != 3 {
				t.Errorf("expected total=3, got %d", total)
			}
			return &service.RecoveryKeysInfo{
				ShareCount: 3,
				Threshold:  2,
				Total:      3,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 2,
		Total:     3,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.ShareCount != 3 {
		t.Errorf("expected ShareCount=3, got %d", resp.ShareCount)
	}
	if resp.Threshold != 2 {
		t.Errorf("expected threshold=2, got %d", resp.Threshold)
	}
	if resp.Total != 3 {
		t.Errorf("expected total=3, got %d", resp.Total)
	}
}

func TestEmbeddedBarrier_RecoveryGenerate_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 2,
		Total:     3,
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryGenerate_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 2,
		Total:     3,
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryGenerate_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierGenRecoveryKeysFn: func(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 2,
		Total:     3,
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierRecoverWithKeys
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_RecoveryRecover(t *testing.T) {
	var capturedKeys []string
	svc := &mockBarrierService{
		barrierRecoverWithKeysFn: func(_ context.Context, keys []string) error {
			capturedKeys = keys
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	keys := []string{"rk-1", "rk-2"}
	err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{
		Keys: keys,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(capturedKeys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(capturedKeys))
	}
	for i, k := range keys {
		if capturedKeys[i] != k {
			t.Errorf("key[%d]: expected %q, got %q", i, k, capturedKeys[i])
		}
	}
}

func TestEmbeddedBarrier_RecoveryRecover_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{
		Keys: []string{"k1"},
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryRecover_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{
		Keys: []string{"k1"},
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BarrierDeleteRecoveryKeys
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_RecoveryDelete(t *testing.T) {
	deleteCalled := false
	svc := &mockBarrierService{
		barrierDeleteRecoveryKeysFn: func(_ context.Context) error {
			deleteCalled = true
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.BarrierDeleteRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !deleteCalled {
		t.Error("expected BarrierDeleteRecoveryKeys to be called on service")
	}
}

func TestEmbeddedBarrier_RecoveryDelete_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierDeleteRecoveryKeys(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryDelete_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierDeleteRecoveryKeys(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BarrierHasRecoveryKeys
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_RecoveryStatus(t *testing.T) {
	svc := &mockBarrierService{
		barrierHasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return true, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !resp.HasKeys {
		t.Error("expected has_keys=true")
	}
}

func TestEmbeddedBarrier_RecoveryStatus_NoKeys(t *testing.T) {
	svc := &mockBarrierService{
		barrierHasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return false, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.HasKeys {
		t.Error("expected has_keys=false")
	}
}

func TestEmbeddedBarrier_RecoveryStatus_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierHasRecoveryKeys(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryStatus_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierHasRecoveryKeys(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryStatus_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierHasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return false, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierHasRecoveryKeys(context.Background())
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierGenerateRootToken
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_RootToken(t *testing.T) {
	createdAt := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		barrierGenRootTokenFn: func(_ context.Context, shares []string) (*service.RootTokenInfo, error) {
			if len(shares) != 3 {
				t.Errorf("expected 3 shares, got %d", len(shares))
			}
			return &service.RootTokenInfo{
				Token:     "root-token-abc123",
				CreatedAt: createdAt,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	resp, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{
		Shares: []string{"s1", "s2", "s3"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.Token != "root-token-abc123" {
		t.Errorf("expected token 'root-token-abc123', got %q", resp.Token)
	}
	expectedCreatedAt := createdAt.Format(time.RFC3339)
	if resp.CreatedAt != expectedCreatedAt {
		t.Errorf("expected created_at %q, got %q", expectedCreatedAt, resp.CreatedAt)
	}
}

func TestEmbeddedBarrier_RootToken_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	_, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{
		Shares: []string{"s1"},
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RootToken_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	_, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{
		Shares: []string{"s1"},
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RootToken_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierGenRootTokenFn: func(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{
		Shares: []string{"s1"},
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Request dispatch handler tests
// ---------------------------------------------------------------------------

func TestEmbeddedBarrier_Request_BarrierInitialize(t *testing.T) {
	var capturedSecret string
	svc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, secret []byte) error {
			capturedSecret = string(secret)
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierInitialize",
		&transport.BarrierInitializeRequest{Secret: []byte("dispatch-secret")}, nil)
	if err != nil {
		t.Fatalf("Request BarrierInitialize failed: %v", err)
	}
	if capturedSecret != "dispatch-secret" {
		t.Errorf("expected secret 'dispatch-secret', got %q", capturedSecret)
	}
}

func TestEmbeddedBarrier_Request_BarrierInitialize_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierInitialize", "not-a-request", nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierStatus(t *testing.T) {
	initTime := time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:         false,
				Strategy:       "shamir",
				HardwareBacked: true,
				InitializedAt:  initTime,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierStatusResponse
	err := tr.Request(context.Background(), "BarrierStatus", nil, &resp)
	if err != nil {
		t.Fatalf("Request BarrierStatus failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Sealed {
		t.Error("expected sealed=false")
	}
	if resp.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", resp.Strategy)
	}
	if !resp.HardwareBacked {
		t.Error("expected hardware_backed=true")
	}
	expectedInit := initTime.Format(time.RFC3339)
	if resp.InitializedAt != expectedInit {
		t.Errorf("expected initialized_at %q, got %q", expectedInit, resp.InitializedAt)
	}
}

func TestEmbeddedBarrier_Request_BarrierUnseal(t *testing.T) {
	var capturedSecret string
	svc := &mockBarrierService{
		barrierUnsealFn: func(_ context.Context, secret []byte) error {
			capturedSecret = string(secret)
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierUnseal",
		&transport.BarrierUnsealRequest{Secret: []byte("unseal-via-dispatch")}, nil)
	if err != nil {
		t.Fatalf("Request BarrierUnseal failed: %v", err)
	}
	if capturedSecret != "unseal-via-dispatch" {
		t.Errorf("expected secret 'unseal-via-dispatch', got %q", capturedSecret)
	}
}

func TestEmbeddedBarrier_Request_BarrierUnseal_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierUnseal", 42, nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierSeal(t *testing.T) {
	sealCalled := false
	svc := &mockBarrierService{
		barrierSealFn: func(_ context.Context) error {
			sealCalled = true
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierSeal", nil, nil)
	if err != nil {
		t.Fatalf("Request BarrierSeal failed: %v", err)
	}
	if !sealCalled {
		t.Error("expected BarrierSeal to be called via dispatch")
	}
}

func TestEmbeddedBarrier_Request_BarrierInitializeShamir(t *testing.T) {
	svc := &mockBarrierService{
		barrierInitializeShamirFn: func(_ context.Context, _ []byte, _, _ int) (*service.ShamirInitInfo, error) {
			return &service.ShamirInitInfo{
				ShareCount:  3,
				Threshold:   2,
				TotalShares: 3,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierInitializeShamirResponse
	err := tr.Request(context.Background(), "BarrierInitializeShamir",
		&transport.BarrierInitializeShamirRequest{Secret: []byte("s"), Threshold: 2, TotalShares: 3}, &resp)
	if err != nil {
		t.Fatalf("Request BarrierInitializeShamir failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ShareCount != 3 {
		t.Errorf("expected ShareCount=3, got %d", resp.ShareCount)
	}
}

func TestEmbeddedBarrier_Request_BarrierInitializeShamir_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierInitializeShamir", "bad", nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierUnsealWithShare(t *testing.T) {
	svc := &mockBarrierService{
		barrierUnsealWithShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return &service.UnsealShareProgress{Required: 3, Submitted: 2, Complete: false}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierUnsealShareResponse
	err := tr.Request(context.Background(), "BarrierUnsealWithShare",
		&transport.BarrierUnsealShareRequest{Share: "s1"}, &resp)
	if err != nil {
		t.Fatalf("Request BarrierUnsealWithShare failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Submitted != 2 {
		t.Errorf("expected submitted=2, got %d", resp.Submitted)
	}
}

func TestEmbeddedBarrier_Request_BarrierUnsealWithShare_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierUnsealWithShare", false, nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierUnsealWithShares(t *testing.T) {
	var capturedShares []string
	svc := &mockBarrierService{
		barrierUnsealWithSharesFn: func(_ context.Context, shares []string) error {
			capturedShares = shares
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierUnsealWithShares",
		&transport.BarrierUnsealSharesRequest{Shares: []string{"a", "b"}}, nil)
	if err != nil {
		t.Fatalf("Request BarrierUnsealWithShares failed: %v", err)
	}
	if len(capturedShares) != 2 {
		t.Errorf("expected 2 shares, got %d", len(capturedShares))
	}
}

func TestEmbeddedBarrier_Request_BarrierUnsealWithShares_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierUnsealWithShares", 99, nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierRekey(t *testing.T) {
	svc := &mockBarrierService{
		barrierRekeyFn: func(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
			return &service.ShamirInitInfo{
				ShareCount:  2,
				Threshold:   1,
				TotalShares: 2,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierRekeyResponse
	err := tr.Request(context.Background(), "BarrierRekey",
		&transport.BarrierRekeyRequest{Threshold: 1, Total: 2}, &resp)
	if err != nil {
		t.Fatalf("Request BarrierRekey failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ShareCount != 2 {
		t.Errorf("expected ShareCount=2, got %d", resp.ShareCount)
	}
}

func TestEmbeddedBarrier_Request_BarrierRekey_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierRekey", "bad", nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierGenerateRecoveryKeys(t *testing.T) {
	svc := &mockBarrierService{
		barrierGenRecoveryKeysFn: func(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
			return &service.RecoveryKeysInfo{
				ShareCount: 2,
				Threshold:  1,
				Total:      2,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierRecoveryKeysResponse
	err := tr.Request(context.Background(), "BarrierGenerateRecoveryKeys",
		&transport.BarrierGenerateRecoveryKeysRequest{Threshold: 1, Total: 2}, &resp)
	if err != nil {
		t.Fatalf("Request BarrierGenerateRecoveryKeys failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ShareCount != 2 {
		t.Errorf("expected ShareCount=2, got %d", resp.ShareCount)
	}
}

func TestEmbeddedBarrier_Request_BarrierGenerateRecoveryKeys_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierGenerateRecoveryKeys", true, nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierRecoverWithKeys(t *testing.T) {
	recoverCalled := false
	svc := &mockBarrierService{
		barrierRecoverWithKeysFn: func(_ context.Context, _ []string) error {
			recoverCalled = true
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierRecoverWithKeys",
		&transport.BarrierRecoverWithKeysRequest{Keys: []string{"k1"}}, nil)
	if err != nil {
		t.Fatalf("Request BarrierRecoverWithKeys failed: %v", err)
	}
	if !recoverCalled {
		t.Error("expected BarrierRecoverWithKeys to be called via dispatch")
	}
}

func TestEmbeddedBarrier_Request_BarrierRecoverWithKeys_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierRecoverWithKeys", "bad", nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_Request_BarrierDeleteRecoveryKeys(t *testing.T) {
	deleteCalled := false
	svc := &mockBarrierService{
		barrierDeleteRecoveryKeysFn: func(_ context.Context) error {
			deleteCalled = true
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierDeleteRecoveryKeys", nil, nil)
	if err != nil {
		t.Fatalf("Request BarrierDeleteRecoveryKeys failed: %v", err)
	}
	if !deleteCalled {
		t.Error("expected BarrierDeleteRecoveryKeys to be called via dispatch")
	}
}

func TestEmbeddedBarrier_Request_BarrierHasRecoveryKeys(t *testing.T) {
	svc := &mockBarrierService{
		barrierHasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return true, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierHasRecoveryKeysResponse
	err := tr.Request(context.Background(), "BarrierHasRecoveryKeys", nil, &resp)
	if err != nil {
		t.Fatalf("Request BarrierHasRecoveryKeys failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.HasKeys {
		t.Error("expected has_keys=true")
	}
}

func TestEmbeddedBarrier_Request_BarrierGenerateRootToken(t *testing.T) {
	createdAt := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		barrierGenRootTokenFn: func(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
			return &service.RootTokenInfo{
				Token:     "dispatch-root-token",
				CreatedAt: createdAt,
			}, nil
		},
	}
	tr := newBarrierTransport(t, svc)

	var resp *transport.BarrierRootTokenResponse
	err := tr.Request(context.Background(), "BarrierGenerateRootToken",
		&transport.BarrierGenerateRootTokenRequest{Shares: []string{"s1", "s2"}}, &resp)
	if err != nil {
		t.Fatalf("Request BarrierGenerateRootToken failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Token != "dispatch-root-token" {
		t.Errorf("expected token 'dispatch-root-token', got %q", resp.Token)
	}
	expectedCreated := createdAt.Format(time.RFC3339)
	if resp.CreatedAt != expectedCreated {
		t.Errorf("expected created_at %q, got %q", expectedCreated, resp.CreatedAt)
	}
}

func TestEmbeddedBarrier_Request_BarrierGenerateRootToken_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierGenerateRootToken", 123, nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BarrierEnableEncryption
// ---------------------------------------------------------------------------

// TestEmbeddedBarrier_EnableEncryption_RefusesTyped is the go-qrdb#558
// transport-matrix test for this transport: this composer has no
// EncryptionMigrator wired into it at all (unlike REST/gRPC/Unix/QUIC/MCP,
// which always expose the endpoint/tool/route and let a nil migrator
// produce a typed refusal at call time), so it refuses unconditionally --
// but with the SAME shared sentinel (service.ErrMigrationProviderUnavailable)
// those transports use for "unavailable", not a transport-specific
// NotImplementedError, so a caller checking for the shared error gets a
// consistent answer regardless of which transport it used.
func TestEmbeddedBarrier_EnableEncryption_RefusesTyped(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	_, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte("pw"),
	})
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("err = %v, want service.ErrMigrationProviderUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// WithBarrierService option test
// ---------------------------------------------------------------------------

func TestWithBarrierService(t *testing.T) {
	svc := &mockBarrierService{}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithBarrierService(svc))
	if err != nil {
		t.Fatalf("failed to create transport with barrier option: %v", err)
	}

	// Verify the barrier service is wired by calling a method that does not
	// return NilServiceError.
	err = tr.BarrierSeal(context.Background())
	if err != nil {
		t.Fatalf("expected no error when barrier service is set, got: %v", err)
	}
}
