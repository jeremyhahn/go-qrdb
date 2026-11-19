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

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock BarrierServicer
// ---------------------------------------------------------------------------

// mockBarrierService implements service.BarrierServicer with function-field
// injection so each test can control the behavior of every method.
type mockBarrierService struct {
	service.BarrierServicer
	barrierInitializeFn         func(ctx context.Context, secret []byte) error
	barrierUnsealFn             func(ctx context.Context, secret []byte) error
	barrierChangePassphraseFn   func(ctx context.Context, current, next []byte) error
	barrierSealFn               func(ctx context.Context) error
	barrierStatusFn             func(ctx context.Context) (*service.BarrierStatusInfo, error)
	barrierClusterUnsealFn      func(ctx context.Context) (*clusterseal.Report, error)
	barrierInitializeShamirFn   func(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	barrierUnsealWithSharesFn   func(ctx context.Context, shares []string) error
	barrierGenRecoveryKeysFn    func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
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

func (m *mockBarrierService) BarrierChangePassphrase(ctx context.Context, current, next []byte) error {
	if m.barrierChangePassphraseFn != nil {
		return m.barrierChangePassphraseFn(ctx, current, next)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.barrierInitializeShamirFn != nil {
		return m.barrierInitializeShamirFn(ctx, threshold, totalShares, policy)
	}
	return &service.ShamirInitInfo{ShareCount: totalShares, Threshold: threshold, TotalShares: totalShares}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.barrierGenRecoveryKeysFn != nil {
		return m.barrierGenRecoveryKeysFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{ShareCount: threshold + 1, Threshold: threshold, Total: threshold + 1}, nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx)
	}
	return &service.BarrierStatusInfo{}, nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.barrierDeleteRecoveryKeysFn != nil {
		return m.barrierDeleteRecoveryKeysFn(ctx)
	}
	return nil
}

// BarrierInitializePrecondition admits every initialize: the double's
// initialize paths answer for themselves through their canned results.
func (m *mockBarrierService) BarrierInitializePrecondition(_ context.Context) error { return nil }

// BarrierClusterUnseal answers as a node without cluster sealing does, unless
// barrierClusterUnsealFn is set.
func (m *mockBarrierService) BarrierClusterUnseal(ctx context.Context) (*clusterseal.Report, error) {
	if m.barrierClusterUnsealFn != nil {
		return m.barrierClusterUnsealFn(ctx)
	}
	return nil, clusterseal.ErrInactive
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.barrierHasRecoveryKeysFn != nil {
		return m.barrierHasRecoveryKeysFn(ctx)
	}
	return false, nil
}

// newBarrierTransport creates a transport with the given barrier service
// injected via the WithBarrierService option.
func newBarrierTransport(t *testing.T, svc service.BarrierServicer) *Transport {
	t.Helper()
	opts := []Option{WithPINBackend(pintest.NewBackend(t))}
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

func TestEmbeddedBarrier_ChangePassphrase_ServiceErrorPassesThrough(t *testing.T) {
	svc := &mockBarrierService{
		barrierChangePassphraseFn: func(context.Context, []byte, []byte) error { return errBarrier },
	}
	tr := newBarrierTransport(t, svc)

	err := tr.BarrierChangePassphrase(context.Background(), &transport.BarrierChangePassphraseRequest{
		Current: []byte("old"),
		Next:    []byte("new"),
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ChangePassphrase_Closed(t *testing.T) {
	tr := closedBarrierTransport(t)

	err := tr.BarrierChangePassphrase(context.Background(), &transport.BarrierChangePassphraseRequest{
		Current: []byte("old"),
		Next:    []byte("new"),
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_ChangePassphrase_NilService(t *testing.T) {
	tr := newBarrierTransport(t, nil)

	err := tr.BarrierChangePassphrase(context.Background(), &transport.BarrierChangePassphraseRequest{
		Current: []byte("old"),
		Next:    []byte("new"),
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

// TestEmbeddedBarrier_ChangePassphrase_RequiresSOPIN verifies the in-process
// passphrase change is SO-gated exactly as on the network transports, against
// the PIN bound by transport.WithSOPIN: no PIN backend fails closed, a missing
// or wrong SO PIN is refused, and in none of those is the barrier service
// reached; the right SO PIN reaches it.
func TestEmbeddedBarrier_ChangePassphrase_RequiresSOPIN(t *testing.T) {
	cases := map[string]struct {
		noBackend bool
		soPIN     string
		want      error
	}{
		"no pin backend": {noBackend: true, soPIN: pintest.ThrottledSOPIN, want: pin.ErrBackendNotConfigured},
		"missing pin":    {soPIN: "", want: pin.ErrPINInvalid},
		"wrong pin":      {soPIN: "wrong-pin-0000", want: pin.ErrPINInvalid},
		"right pin":      {soPIN: pintest.ThrottledSOPIN},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			svc := &mockBarrierService{
				barrierChangePassphraseFn: func(context.Context, []byte, []byte) error {
					called = true
					return nil
				},
			}
			opts := []Option{WithBarrierService(svc)}
			if !tc.noBackend {
				opts = append(opts, WithPINBackend(pintest.NewSOPINBackend(t)))
			}
			tr, err := New(newMockKVService(), &mockAdminService{}, opts...)
			if err != nil {
				t.Fatalf("failed to create transport: %v", err)
			}
			ctx, release, err := transport.WithSOPIN(context.Background(), []byte(tc.soPIN))
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			err = tr.BarrierChangePassphrase(ctx, &transport.BarrierChangePassphraseRequest{
				Current: []byte("old-passphrase"),
				Next:    []byte("new-passphrase"),
			})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if reached := tc.want == nil; called != reached {
				t.Fatalf("service reached = %v, want %v", called, reached)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BarrierSeal
// ---------------------------------------------------------------------------

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
		barrierInitializeShamirFn: func(_ context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
			if policy != seal.FactorPolicyAnyEnrolled {
				t.Errorf("expected policy any-enrolled, got %q", policy)
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
		Threshold:   3,
		TotalShares: 5,
		Policy:      transport.PolicyAnyEnrolled,
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
		barrierInitializeShamirFn: func(_ context.Context, _, _ int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Threshold:   2,
		TotalShares: 3,
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
		barrierGenRecoveryKeysFn: func(_ context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
			if threshold != 2 {
				t.Errorf("expected threshold=2, got %d", threshold)
			}
			if policy != seal.FactorPolicyDesignated {
				t.Errorf("expected policy designated, got %q", policy)
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
		Policy:    transport.PolicyDesignated,
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
	})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedBarrier_RecoveryGenerate_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		barrierGenRecoveryKeysFn: func(_ context.Context, _ int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
			return nil, errBarrier
		},
	}
	tr := newBarrierTransport(t, svc)

	_, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 2,
	})
	if !errors.Is(err, errBarrier) {
		t.Errorf("expected errBarrier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierDeleteRecoveryKeys
// ---------------------------------------------------------------------------

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

func TestEmbeddedBarrier_Request_BarrierChangePassphrase(t *testing.T) {
	var called bool
	svc := &mockBarrierService{
		barrierChangePassphraseFn: func(_ context.Context, current, next []byte) error {
			called = string(current) == "old-via-dispatch" && string(next) == "new-via-dispatch"
			return nil
		},
	}
	tr := newBarrierTransport(t, svc)

	err := tr.Request(context.Background(), "BarrierChangePassphrase",
		&transport.BarrierChangePassphraseRequest{
			Current: []byte("old-via-dispatch"),
			Next:    []byte("new-via-dispatch"),
		}, nil)
	if err != nil {
		t.Fatalf("Request BarrierChangePassphrase failed: %v", err)
	}
	if !called {
		t.Error("the service did not receive the dispatched passphrases")
	}
}

func TestEmbeddedBarrier_Request_BarrierChangePassphrase_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierChangePassphrase", 42, nil)
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

func TestEmbeddedBarrier_Request_BarrierInitializeShamir_InvalidRequest(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})

	err := tr.Request(context.Background(), "BarrierInitializeShamir", "bad", nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T: %v", err, err)
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

// ---------------------------------------------------------------------------
// BarrierEnableEncryption
// ---------------------------------------------------------------------------

// stubEmbeddedMigrator is a minimal service.EncryptionMigrator test double
// for BarrierEnableEncryption tests.
type stubEmbeddedMigrator struct {
	ch  chan embeddedMigrateCall
	err error
}

type embeddedMigrateCall struct {
	ctx   context.Context // the context the migration runs on
	scope service.BarrierScope
	opts  seal.MigrationOptions
}

func (m *stubEmbeddedMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	m.ch <- embeddedMigrateCall{ctx: ctx, scope: scope, opts: opts}
	return m.err
}

// TestEmbeddedBarrier_EnableEncryption_RefusesTyped proves that with no
// EncryptionMigrator configured (the default -- WithEncryptionMigrator was
// never supplied), BarrierEnableEncryption refuses with the SAME shared
// sentinel (service.ErrMigrationProviderUnavailable) REST/gRPC/Unix/QUIC/MCP
// use for "unavailable", not a transport-specific NotImplementedError, so a
// caller checking for the shared error gets a consistent answer regardless
// of which transport it used.
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

// TestEmbeddedBarrier_EnableEncryption_SystemStarts proves that with an
// EncryptionMigrator configured via WithEncryptionMigrator (the wiring
// sdk/go/node supplies from its own live migrator), BarrierEnableEncryption
// actually reaches it and reports "migration_started", matching the
// REST/gRPC/Unix/QUIC/MCP handlers.
func TestEmbeddedBarrier_EnableEncryption_SystemStarts(t *testing.T) {
	mig := &stubEmbeddedMigrator{ch: make(chan embeddedMigrateCall, 1)}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(mig))
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}

	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte("pw"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "migration_started" {
		t.Fatalf("Status = %q, want migration_started", resp.Status)
	}
	if resp.Scope != "system" {
		t.Fatalf("Scope = %q, want system", resp.Scope)
	}

	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeSystem {
			t.Fatalf("scope.Kind = %v, want ScopeSystem", call.scope.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

// TestEmbeddedBarrier_EnableEncryption_InvalidOptionsRejectedSynchronously
// proves invalid options are rejected before the migrator is ever invoked.
func TestEmbeddedBarrier_EnableEncryption_InvalidOptionsRejectedSynchronously(t *testing.T) {
	mig := &stubEmbeddedMigrator{ch: make(chan embeddedMigrateCall, 1)}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(mig))
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}

	// strategy "none" is not a valid migration target.
	_, err = tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy: "none",
	})
	if err == nil {
		t.Fatal("expected an error for strategy=none")
	}
	select {
	case <-mig.ch:
		t.Fatal("migrator must not be invoked for invalid options")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestEmbeddedBarrier_EnableEncryption_Closed proves a closed transport
// refuses before ever consulting the migrator.
func TestEmbeddedBarrier_EnableEncryption_Closed(t *testing.T) {
	mig := &stubEmbeddedMigrator{ch: make(chan embeddedMigrateCall, 1)}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(mig))
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte("pw"),
	})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}

// ---------------------------------------------------------------------------
// WithBarrierService option test
// ---------------------------------------------------------------------------

// TestEmbeddedBarrier_Request_RetiredShareVerbsAreUnknown proves the raw-share
// verbs the unified share-holder quorum retired are gone from the dispatch
// table: every share now reaches the barrier through BarrierQuorumSubmit from
// an authenticated holder.
func TestEmbeddedBarrier_Request_RetiredShareVerbsAreUnknown(t *testing.T) {
	tr := newBarrierTransport(t, &mockBarrierService{})
	for _, method := range []string{
		"BarrierUnsealWithShare", "BarrierUnsealWithShares", "BarrierRekey",
		"BarrierRecoverWithKeys", "BarrierGenerateRootToken", "BarrierShamirSealVote",
	} {
		var unknown *UnknownMethodError
		if err := tr.Request(context.Background(), method, true, nil); !errors.As(err, &unknown) {
			t.Errorf("Request(%s) = %v, want UnknownMethodError", method, err)
		}
	}
}

// TestBarrierClusterUnseal_Embedded: the embedded transport forwards cluster
// unseal to the service and converts its report, refuses it while inactive,
// and carries the node barrier's cluster seal on status.
func TestBarrierClusterUnseal_Embedded(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tr := newBarrierTransport(t, &mockBarrierService{
		barrierClusterUnsealFn: func(context.Context) (*clusterseal.Report, error) {
			return &clusterseal.Report{Active: true, Epoch: 4, Principal: "ops", PrincipalTenant: "system",
				Node: "192.0.2.1:5000", At: at, Local: clusterseal.LocalServing}, nil
		},
		barrierStatusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{ClusterSeal: &clusterseal.Report{Active: true, Sealed: true, Epoch: 3, Local: clusterseal.LocalHeld}}, nil
		},
	})
	got, err := tr.BarrierClusterUnseal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := transport.ClusterSeal{Active: true, Epoch: 4, Principal: "ops", PrincipalTenant: "system",
		Node: "192.0.2.1:5000", At: "2026-10-01T12:00:00Z", Local: "serving"}
	if *got != want {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
	st, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ClusterSeal == nil || !st.ClusterSeal.Sealed || st.ClusterSeal.Local != "held" {
		t.Fatalf("ClusterSeal = %+v", st.ClusterSeal)
	}
	if _, err := newBarrierTransport(t, &mockBarrierService{}).BarrierClusterUnseal(context.Background()); !errors.Is(err, clusterseal.ErrInactive) {
		t.Fatalf("inactive: err = %v", err)
	}
}
