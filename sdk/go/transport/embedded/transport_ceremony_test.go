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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedCeremony_Initialize(t *testing.T) {
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if resp.SPKIPin != "pin" || resp.State != "initialized" {
		t.Fatalf("unexpected: %+v", resp)
	}
	if resp.ShareCount != 3 {
		t.Fatalf("ShareCount = %d", resp.ShareCount)
	}
}

// The CeremonyCA installed by WithCeremonyCA is the one the ceremony service
// is handed.
func TestEmbeddedCeremony_Initialize_ForwardsConfiguredCA(t *testing.T) {
	ca := &mockCeremonyCA{}
	var got ceremony.CeremonyCA
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{
			initFn: func(_ context.Context, c ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
				got = c
				return &service.CeremonyInitResult{State: "initialized"}, nil
			},
		}),
		WithCeremonyCA(ca),
	)
	if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{}); err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if got != ca {
		t.Fatalf("service received CA %v, want the configured one", got)
	}
}

// TestEmbeddedCeremony_Initialize_NilCAReachesTheService proves a transport
// with a ceremony service but no CeremonyCA (WithCeremonyCA never applied,
// as a BYO-TLS deployment configures it) reaches the ceremony service layer
// with ca == nil rather than being refused by the transport.
// ceremony.CeremonyService.Initialize is nil-safe by construction -- a
// BYO-TLS node pins its own configured leaf instead of self-issuing one --
// so refusing here (the pre-fix behavior, go-qrdb#560 review item 5) only
// blocked `qrdb init` on a BYO-TLS embedded deployment, for no safety
// reason.
func TestEmbeddedCeremony_Initialize_NilCAReachesTheService(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t, WithCeremonyService(mock))
	if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mock.initOptsSeen {
		t.Fatal("expected the service to be reached with a nil CA (BYO-TLS)")
	}
}

// TestEmbeddedCeremony_Initialize_ForwardsPins proves the embedded transport
// no longer discards the request: the SO and user PINs supplied on the
// CeremonyInitializeRequest reach the service layer as CeremonyInitializeOptions.
// This is the regression guard for the gap where the embedded transport called
// the service with the injected CA only and dropped req entirely.
func TestEmbeddedCeremony_Initialize_ForwardsPins(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t,
		WithCeremonyService(mock),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin:   "so-secret-pin",
		UserPin: "user-secret-pin",
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if !mock.initOptsSeen {
		t.Fatal("service was not invoked")
	}
	if mock.lastInitOpts == nil {
		t.Fatal("expected non-nil CeremonyInitializeOptions to reach the service; pins were dropped")
	}
	if mock.lastInitOpts.SOPin != "so-secret-pin" {
		t.Fatalf("SOPin not forwarded: got %q", mock.lastInitOpts.SOPin)
	}
	if mock.lastInitOpts.UserPin != "user-secret-pin" {
		t.Fatalf("UserPin not forwarded: got %q", mock.lastInitOpts.UserPin)
	}
}

// TestEmbeddedCeremony_Initialize_ForwardsAllFields is the regression guard
// for the bug where the embedded transport only forwarded SOPin/UserPin into
// service.CeremonyInitializeOptions, silently dropping
// CredentialSealStrategy, Threshold, Officers, Hostname, and SANs from the
// CeremonyInitializeRequest.
func TestEmbeddedCeremony_Initialize_ForwardsAllFields(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t,
		WithCeremonyService(mock),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin:                  "so-pin",
		UserPin:                "user-pin",
		CredentialSealStrategy: "shamir",
		Threshold:              2,
		Officers: []transport.CeremonyOfficerRequest{
			{Username: "officer-1", CSRPEM: "csr1"},
			{Username: "officer-2", CSRPEM: "csr2"},
		},
		Hostname: "override.example",
		SANs:     []string{"alt1.example", "alt2.example"},
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}

	got := mock.lastInitOpts
	if got == nil {
		t.Fatal("expected non-nil CeremonyInitializeOptions to reach the service")
	}
	if got.CredentialSealStrategy != "shamir" {
		t.Errorf("CredentialSealStrategy = %q, want %q", got.CredentialSealStrategy, "shamir")
	}
	if got.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", got.Threshold)
	}
	if got.Hostname != "override.example" {
		t.Errorf("Hostname = %q, want %q", got.Hostname, "override.example")
	}
	if len(got.SANs) != 2 || got.SANs[0] != "alt1.example" {
		t.Errorf("SANs = %v", got.SANs)
	}
	if len(got.Officers) != 2 || got.Officers[0].Username != "officer-1" || got.Officers[0].CSRPEM != "csr1" {
		t.Errorf("Officers = %+v", got.Officers)
	}
}

// TestEmbeddedCeremony_Initialize_WipesAdminKeyPEM proves the generate
// path's private key is wiped from the service-layer result
// (CeremonyInitResult.WipeAdminKeyPEM) once this transport has built its
// own, independent copy for the response -- go-qrdb#560 review item 7:
// "WipeAdminKeyPEM runs on every transport, not only REST." The caller
// must still receive the correct, un-zeroed key: proving the wipe happens
// AFTER holderwire.CeremonyInitTo's bytes.Clone, never before it.
func TestEmbeddedCeremony_Initialize_WipesAdminKeyPEM(t *testing.T) {
	result := &service.CeremonyInitResult{
		SPKIPin:               "pin",
		State:                 "initialized",
		AdminCertPEM:          []byte("admin-cert"),
		AdminKeyPEM:           []byte("admin-key"),
		AdminCredentialSource: "generated",
	}
	mock := &mockCeremonyService{
		initFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return result, nil
		},
	}
	tr := newExtTransport(t, WithCeremonyService(mock), WithCeremonyCA(&mockCeremonyCA{}))

	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if string(resp.AdminKeyPEM) != "admin-key" {
		t.Fatalf("caller must still receive the un-zeroed key, got %q", resp.AdminKeyPEM)
	}
	if result.AdminKeyPEM != nil {
		t.Fatalf("expected the service result's AdminKeyPEM to be wiped (nil) after the call, got %q", result.AdminKeyPEM)
	}
}

// TestEmbeddedCeremony_Initialize_NilRequest ensures the embedded transport
// rejects a nil request rather than dereferencing it.
func TestEmbeddedCeremony_Initialize_NilRequest(t *testing.T) {
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	_, err := tr.CeremonyInitialize(context.Background(), nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Fatalf("expected InvalidRequestError, got %T (%v)", err, err)
	}
}

func TestEmbeddedCeremony_Status(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{}))
	resp, err := tr.CeremonyStatus(context.Background())
	if err != nil {
		t.Fatalf("CeremonyStatus: %v", err)
	}
	if resp.State != "initialized" || resp.SPKIPin != "pin" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedCeremony_SignCSR(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{}))
	resp, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{
		Username: "alice",
		SOPin:    "1234",
		CSRPEM:   "csr",
		Role:     "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	if resp.CertPEM != "CERT" {
		t.Fatalf("CertPEM = %q", resp.CertPEM)
	}
}

// ceremonyCalls drives every ceremony verb of tr once.
func ceremonyCalls(tr *Transport) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"CeremonyInitialize": func() error {
			_, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{})
			return err
		},
		"CeremonyStatus": func() error { _, err := tr.CeremonyStatus(ctx); return err },
		"CeremonySignCSR": func() error {
			_, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{})
			return err
		},
	}
}

// Every ceremony verb answers a transport built without a ceremony service
// with the typed wiring-defect error, even when a CA is configured.
func TestEmbeddedCeremony_NilServiceIsTyped(t *testing.T) {
	for name, call := range ceremonyCalls(newExtTransport(t, WithCeremonyCA(&mockCeremonyCA{}))) {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: err = %T (%v), want *NilServiceError", name, err, err)
		}
	}
}

// Every ceremony verb on a closed transport is refused with
// *transport.ClosedError.
func TestEmbeddedCeremony_ClosedIsRefused(t *testing.T) {
	tr := closedExtTransport(t, WithCeremonyService(&mockCeremonyService{}), WithCeremonyCA(&mockCeremonyCA{}))
	for name, call := range ceremonyCalls(tr) {
		var ce *transport.ClosedError
		if err := call(); !errors.As(err, &ce) {
			t.Errorf("%s: err = %T (%v), want *transport.ClosedError", name, err, err)
		}
	}
}

// A ceremony service failure reaches the caller unchanged from every verb.
func TestEmbeddedCeremony_ServiceErrorPropagates(t *testing.T) {
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{
			initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
				return nil, errSentinel
			},
			statusFn: func(context.Context) (*service.CeremonyStatusInfo, error) { return nil, errSentinel },
			signCSRFn: func(context.Context, string, string, string, string) (*service.CeremonySignCSRResult, error) {
				return nil, errSentinel
			},
		}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	for name, call := range ceremonyCalls(tr) {
		if err := call(); !errors.Is(err, errSentinel) {
			t.Errorf("%s: err = %v, want the service's error", name, err)
		}
	}
}
