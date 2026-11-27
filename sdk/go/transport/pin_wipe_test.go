package transport

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// zeroed reports whether every byte of b is zero.
func zeroed(b []byte) bool {
	return bytes.Equal(b, make([]byte, len(b)))
}

// TestWipePINs_ZeroesEveryRequestPIN asserts each request's WipePINs zeroes
// the caller's PIN bytes in place, and that a nil request wipes without
// panicking.
func TestWipePINs_ZeroesEveryRequestPIN(t *testing.T) {
	tests := []struct {
		name string
		// build returns the request's wiper and the PIN slices it must zero.
		build  func() (interface{ WipePINs() }, [][]byte)
		nilReq interface{ WipePINs() }
	}{
		{
			name: "BarrierInitializeRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so, user := []byte("so-pin-1"), []byte("user-pin")
				return &BarrierInitializeRequest{SOPin: so, UserPin: user}, [][]byte{so, user}
			},
			nilReq: (*BarrierInitializeRequest)(nil),
		},
		{
			name: "BarrierInitializeShamirRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so, user := []byte("so-pin-2"), []byte("user-pin")
				return &BarrierInitializeShamirRequest{SOPin: so, UserPin: user}, [][]byte{so, user}
			},
			nilReq: (*BarrierInitializeShamirRequest)(nil),
		},
		{
			name: "BarrierGenerateRecoveryKeysRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so := []byte("so-pin-3")
				return &BarrierGenerateRecoveryKeysRequest{SOPin: so}, [][]byte{so}
			},
			nilReq: (*BarrierGenerateRecoveryKeysRequest)(nil),
		},
		{
			name: "RekeyStartRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so := []byte("so-pin-4")
				return &RekeyStartRequest{SOPin: so}, [][]byte{so}
			},
			nilReq: (*RekeyStartRequest)(nil),
		},
		{
			name: "OfficerRevokeRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so := []byte("so-pin-5")
				return &OfficerRevokeRequest{SOPin: so}, [][]byte{so}
			},
			nilReq: (*OfficerRevokeRequest)(nil),
		},
		{
			name: "CeremonyInitializeRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so, user := []byte("so-pin-6"), []byte("user-pin")
				return &CeremonyInitializeRequest{SOPin: so, UserPin: user}, [][]byte{so, user}
			},
			nilReq: (*CeremonyInitializeRequest)(nil),
		},
		{
			name: "SignCSRInitRequest",
			build: func() (interface{ WipePINs() }, [][]byte) {
				so := []byte("so-pin-7")
				return &SignCSRInitRequest{SOPin: so}, [][]byte{so}
			},
			nilReq: (*SignCSRInitRequest)(nil),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, pins := tt.build()
			req.WipePINs()
			for i, p := range pins {
				if !zeroed(p) {
					t.Fatalf("PIN %d left %q after WipePINs", i, p)
				}
			}
			tt.nilReq.WipePINs() // a nil request wipes without panicking
		})
	}
}

// TestBarrierSecret_PassphraseCarriers covers the passphrase-kind requests
// whose secret classification the row's byte PINs route through.
func TestBarrierSecret_PassphraseCarriers(t *testing.T) {
	tests := []struct {
		name string
		got  BarrierSecretKind
		want BarrierSecretKind
	}{
		{"change passphrase nil", (*BarrierChangePassphraseRequest)(nil).barrierSecret(), ""},
		{"change passphrase empty", (&BarrierChangePassphraseRequest{}).barrierSecret(), ""},
		{"change passphrase current", (&BarrierChangePassphraseRequest{Current: []byte("old")}).barrierSecret(), BarrierSecretPassphrase},
		{"change passphrase next", (&BarrierChangePassphraseRequest{Next: []byte("new")}).barrierSecret(), BarrierSecretPassphrase},
		{"signed init complete nil", (*TenantBarrierSignedInitCompleteRequest)(nil).barrierSecret(), ""},
		{"signed init complete empty", (&TenantBarrierSignedInitCompleteRequest{}).barrierSecret(), ""},
		{"signed init complete passphrase", (&TenantBarrierSignedInitCompleteRequest{Passphrase: []byte("pw")}).barrierSecret(), BarrierSecretPassphrase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("barrierSecret() = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

// TestSOPINRedirectError_Message names the transport and the refused status.
func TestSOPINRedirectError_Message(t *testing.T) {
	msg := (&SOPINRedirectError{Transport: "rest", Status: http.StatusTemporaryRedirect}).Error()
	for _, want := range []string{"rest", "307", http.StatusText(http.StatusTemporaryRedirect), "never resent"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Error() = %q, missing %q", msg, want)
		}
	}
}

// TestReconcileFailure covers the error a transport returns beside a
// deleted-tenant reconcile response.
func TestReconcileFailure(t *testing.T) {
	if err := ReconcileFailure(nil, nil); err != nil {
		t.Fatalf("nil response: %v", err)
	}
	if err := ReconcileFailure(&ReconcileDeletedTenantsResponse{}, nil); err != nil {
		t.Fatalf("no failures: %v", err)
	}
	cause := errors.New("node cause")
	resp := &ReconcileDeletedTenantsResponse{Failed: []TenantResidueFailure{
		{TenantID: "t1", Op: "remove", Path: "/data/t1", Error: "busy"},
		{TenantID: "t2", Op: "close-runtime", Error: "timeout"},
	}}
	err := ReconcileFailure(resp, cause)
	var rerr *ReconcileDeletedTenantsError
	if !errors.As(err, &rerr) || len(rerr.Failed) != 2 {
		t.Fatalf("err = %v, want *ReconcileDeletedTenantsError with 2 failures", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err does not wrap the cause")
	}
	want := "transport: reconcile deleted tenants: 2 failed; tenant t1: remove /data/t1: busy; tenant t2: close-runtime: timeout"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
