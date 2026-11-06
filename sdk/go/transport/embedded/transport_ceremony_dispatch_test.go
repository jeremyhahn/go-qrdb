// Copyright (c) 2026 Jeremy Hahn
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
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequest_CeremonyDispatch drives the ceremony verbs through the generic
// Request dispatch: each writes its result through the response pointer, a
// request of the wrong type is refused, and the service's refusal comes back
// unchanged.
func TestRequest_CeremonyDispatch(t *testing.T) {
	ctx := context.Background()
	svc := &mockCeremonyService{}
	tr := newExtTransport(t, WithCeremonyService(svc), WithCeremonyCA(&mockCeremonyCA{}))

	var initOut *transport.CeremonyInitializeResponse
	if err := tr.Request(ctx, "CeremonyInitialize", &transport.CeremonyInitializeRequest{SOPin: "so-pin-1234", UserPin: "user-pin-1234", Threshold: 2}, &initOut); err != nil {
		t.Fatal(err)
	}
	if initOut == nil || initOut.State != "initialized" || initOut.ShareCount != 3 {
		t.Fatalf("initialize response = %+v", initOut)
	}
	if svc.lastInitOpts == nil || svc.lastInitOpts.SOPin != "so-pin-1234" || svc.lastInitOpts.Threshold != 2 {
		t.Fatalf("the service saw %+v, want the request's SO PIN and threshold", svc.lastInitOpts)
	}

	var statusOut *transport.CeremonyStatusResponse
	if err := tr.Request(ctx, "CeremonyStatus", nil, &statusOut); err != nil {
		t.Fatal(err)
	}
	if statusOut == nil || statusOut.State != "initialized" || statusOut.SPKIPin != "pin" {
		t.Fatalf("status response = %+v", statusOut)
	}

	var signOut *transport.SignCSRInitResponse
	if err := tr.Request(ctx, "CeremonySignCSR", &transport.SignCSRInitRequest{Username: "alice", SOPin: "so-pin-1234", CSRPEM: "csr"}, &signOut); err != nil {
		t.Fatal(err)
	}
	if signOut == nil || signOut.CertPEM != "CERT" {
		t.Fatalf("sign-csr response = %+v", signOut)
	}

	for _, method := range []string{"CeremonyInitialize", "CeremonySignCSR"} {
		var invalid *InvalidRequestError
		if err := tr.Request(ctx, method, "not-the-request", nil); !errors.As(err, &invalid) || invalid.Method != method {
			t.Errorf("%s with the wrong request type = %v, want InvalidRequestError", method, err)
		}
	}
}

// TestRequest_CeremonyDispatch_ServiceRefusals verifies a refusal by the
// ceremony service reaches the caller through the dispatch, for each verb.
func TestRequest_CeremonyDispatch_ServiceRefusals(t *testing.T) {
	ctx := context.Background()
	svc := &mockCeremonyService{
		initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return nil, ceremony.ErrAlreadyInitialized
		},
		statusFn: func(context.Context) (*service.CeremonyStatusInfo, error) {
			return nil, ceremony.ErrNotInitializedOrEnrolling
		},
		signCSRFn: func(context.Context, string, string, string, string) (*service.CeremonySignCSRResult, error) {
			return nil, ceremony.ErrNotInitializedOrEnrolling
		},
	}
	tr := newExtTransport(t, WithCeremonyService(svc), WithCeremonyCA(&mockCeremonyCA{}))
	cases := map[string]struct {
		req  any
		want error
	}{
		"CeremonyInitialize": {&transport.CeremonyInitializeRequest{}, ceremony.ErrAlreadyInitialized},
		"CeremonyStatus":     {nil, ceremony.ErrNotInitializedOrEnrolling},
		"CeremonySignCSR":    {&transport.SignCSRInitRequest{Username: "alice"}, ceremony.ErrNotInitializedOrEnrolling},
	}
	for method, c := range cases {
		if err := tr.Request(ctx, method, c.req, nil); !errors.Is(err, c.want) {
			t.Errorf("%s = %v, want %v", method, err, c.want)
		}
	}
}

// TestCeremonySignCSR_NilRequest verifies a nil sign-csr request is refused
// as invalid, as an initialize is, rather than dereferenced -- directly and
// through the dispatch, where a typed nil passes the request type check.
func TestCeremony_NilRequest(t *testing.T) {
	ctx := context.Background()
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{}), WithCeremonyCA(&mockCeremonyCA{}))
	tests := []struct {
		name, method string
		call         func() error
	}{
		{"CeremonyInitialize", "CeremonyInitialize", func() error { _, err := tr.CeremonyInitialize(ctx, nil); return err }},
		{"CeremonySignCSR", "CeremonySignCSR", func() error { _, err := tr.CeremonySignCSR(ctx, nil); return err }},
		{"Request/CeremonySignCSR", "CeremonySignCSR", func() error {
			return tr.Request(ctx, "CeremonySignCSR", (*transport.SignCSRInitRequest)(nil), nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if invalid, ok := errors.AsType[*InvalidRequestError](tt.call()); !ok || invalid.Method != tt.method {
				t.Fatalf("nil request = %v, want InvalidRequestError for %s", invalid, tt.method)
			}
		})
	}
}

// TestEmbeddedCeremony_Initialize_ForwardsAllFields is the regression guard
// for the embedded transport dropping the request, then forwarding only
// SOPin/UserPin: every field of the CeremonyInitializeRequest reaches the
// service as CeremonyInitializeOptions.
func TestEmbeddedCeremony_Initialize_ForwardsAllFields(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t, WithCeremonyService(mock), WithCeremonyCA(&mockCeremonyCA{}))

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
	if got.SOPin != "so-pin" || got.UserPin != "user-pin" {
		t.Errorf("SOPin, UserPin = %q, %q", got.SOPin, got.UserPin)
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
	if mock.lastInitOpts == nil {
		t.Fatal("expected the service to be reached with a nil CA (BYO-TLS)")
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
		initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
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

// mockCeremonyService answers every ceremony call with a fixed result and
// records the options CeremonyInitialize received; a non-nil Fn field
// replaces the answer.
type mockCeremonyService struct {
	initFn       func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	lastInitOpts *service.CeremonyInitializeOptions
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
}

var _ service.CeremonyServicer = (*mockCeremonyService)(nil)

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.lastInitOpts = opts
	if m.initFn != nil {
		return m.initFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "pin",
		CACertPEM:  []byte("CA"),
		TLSCertPEM: []byte("TLS"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("CERT")}, nil
}

func (m *mockCeremonyService) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin"}, nil
}

// mockCeremonyCA satisfies ceremony.CeremonyCA for a transport whose
// ceremony service never calls it.
type mockCeremonyCA struct{}

var _ ceremony.CeremonyCA = (*mockCeremonyCA)(nil)

func (m *mockCeremonyCA) SignCSR(context.Context, []byte, *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) CABundle() ([]byte, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) IssueCertificate(context.Context, *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, errors.New("not used")
}
