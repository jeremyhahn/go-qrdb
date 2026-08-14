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

//go:build !integration

// bindings_officer_test.go verifies the OfficerBinding Wails surface: SO
// enrollment (system, recovery, tenant), the per-SO share-claim ceremony,
// and quorum seal voting — including the PEM string<->[]byte DTO
// conversions that keep PEM plain text across the Wails IPC boundary.
package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// errOfficerTest is the sentinel injected through the mock services to
// exercise the SDK-call failure path of every binding method.
var errOfficerTest = errors.New("officer test failure")

// newTestOfficerBinding wires an OfficerBinding to an embedded transport
// backed by the given barrier and tenant service mocks.
func newTestOfficerBinding(t *testing.T, barrierSvc service.BarrierServicer, tenantSvc tenant.TenantServicer) *OfficerBinding {
	t.Helper()
	conn := newTestConnectionBinding(
		t,
		newMockKVService(),
		newMockAdminService(),
		embedded.WithBarrierService(barrierSvc),
		embedded.WithTenantService(tenantSvc),
	)
	return NewOfficerBinding(testLogger(t), conn)
}

// disconnectedOfficerBinding returns an OfficerBinding whose connection has
// no client, so every method must fail with NotConnectedError.
func disconnectedOfficerBinding(t *testing.T) *OfficerBinding {
	t.Helper()
	return NewOfficerBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func TestNewOfficerBinding_ReturnsBinding(t *testing.T) {
	b := NewOfficerBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	if b == nil {
		t.Fatal("expected non-nil OfficerBinding")
	}
}

func TestNewOfficerBinding_UsesProvidedConnection(t *testing.T) {
	b := newTestOfficerBinding(t, &mockBarrierService{}, &mockTenantService{})
	if _, err := b.ListOfficers(); err != nil {
		t.Fatalf("expected wired connection to serve ListOfficers, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Not-connected path: every method must surface NotConnectedError
// ---------------------------------------------------------------------------

func TestOfficerBinding_NotConnected_AllMethods(t *testing.T) {
	b := disconnectedOfficerBinding(t)
	calls := map[string]func() error{
		"RegisterOfficer": func() error {
			_, err := b.RegisterOfficer(&OfficerRegisterParams{Username: "so1"})
			return err
		},
		"ListOfficers": func() error {
			_, err := b.ListOfficers()
			return err
		},
		"OfficerClaimStatus": func() error {
			_, err := b.OfficerClaimStatus()
			return err
		},
		"ClaimBegin": func() error {
			_, err := b.ClaimBegin(&transport.ShareClaimBeginRequest{Username: "so1"})
			return err
		},
		"ClaimComplete": func() error {
			_, err := b.ClaimComplete(&transport.ShareClaimCompleteRequest{Username: "so1"})
			return err
		},
		"RegisterRecoveryOfficer": func() error {
			_, err := b.RegisterRecoveryOfficer(&OfficerRegisterParams{Username: "so1"})
			return err
		},
		"RecoveryClaimBegin": func() error {
			_, err := b.RecoveryClaimBegin(&transport.ShareClaimBeginRequest{Username: "so1"})
			return err
		},
		"RecoveryClaimComplete": func() error {
			_, err := b.RecoveryClaimComplete(&transport.ShareClaimCompleteRequest{Username: "so1"})
			return err
		},
		"SubmitSealVote": func() error {
			_, err := b.SubmitSealVote(&transport.ShamirSealVoteRequest{Share: "s"})
			return err
		},
		"ListTenantOfficers": func() error {
			_, err := b.ListTenantOfficers("acme")
			return err
		},
		"TenantOfficerClaimStatus": func() error {
			_, err := b.TenantOfficerClaimStatus("acme")
			return err
		},
		"RegisterTenantOfficer": func() error {
			_, err := b.RegisterTenantOfficer(&TenantOfficerRegisterParams{TenantID: "acme", Username: "so1"})
			return err
		},
		"TenantClaimBegin": func() error {
			_, err := b.TenantClaimBegin(&transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "so1"})
			return err
		},
		"TenantClaimComplete": func() error {
			_, err := b.TenantClaimComplete(&transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "so1"})
			return err
		},
		"SubmitTenantSealVote": func() error {
			_, err := b.SubmitTenantSealVote(&transport.TenantShamirSealVoteRequest{TenantID: "acme", Share: "s"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("expected error on disconnected binding")
			}
			var nce *NotConnectedError
			if !errors.As(err, &nce) {
				t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RegisterOfficer
// ---------------------------------------------------------------------------

func TestOfficerBinding_RegisterOfficer_Success(t *testing.T) {
	var gotReq *service.BarrierOfficerRegisterRequest
	svc := &mockBarrierService{
		officerRegisterFn: func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			gotReq = req
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("-----BEGIN CERTIFICATE-----"), ShareIndex: 3}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.RegisterOfficer(&OfficerRegisterParams{
		Username:          "so1",
		CSRPEM:            "-----BEGIN CERTIFICATE REQUEST-----",
		AuthenticatorType: "piv",
		AuthenticatorRef:  "slot-9a",
	})
	if err != nil {
		t.Fatalf("RegisterOfficer: %v", err)
	}
	if gotReq.Username != "so1" {
		t.Fatalf("expected username so1, got %q", gotReq.Username)
	}
	// The PEM string from the Wails DTO must arrive at the service as bytes.
	if !bytes.Equal(gotReq.CSRPEM, []byte("-----BEGIN CERTIFICATE REQUEST-----")) {
		t.Fatalf("expected CSR PEM bytes, got %q", gotReq.CSRPEM)
	}
	if gotReq.AuthenticatorType != "piv" || gotReq.AuthenticatorRef != "slot-9a" {
		t.Fatalf("expected authenticator fields forwarded, got %q/%q", gotReq.AuthenticatorType, gotReq.AuthenticatorRef)
	}
	// The certificate bytes must return to the frontend as a PEM string.
	if resp.CertPEM != "-----BEGIN CERTIFICATE-----" {
		t.Fatalf("expected cert PEM string, got %q", resp.CertPEM)
	}
	if resp.ShareIndex != 3 {
		t.Fatalf("expected share index 3, got %d", resp.ShareIndex)
	}
}

func TestOfficerBinding_RegisterOfficer_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		officerRegisterFn: func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.RegisterOfficer(&OfficerRegisterParams{Username: "so1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListOfficers
// ---------------------------------------------------------------------------

func TestOfficerBinding_ListOfficers_Success(t *testing.T) {
	enrolled := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		officerListFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return []*service.BarrierOfficerStatus{
				{Username: "so1", CertThumbprint: "tp1", ShareIndex: 1, AuthFactor: "piv", EnrolledAt: enrolled, SharePending: true},
				{Username: "so2", ShareIndex: 2, AuthFactor: "software"},
			}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	officers, err := b.ListOfficers()
	if err != nil {
		t.Fatalf("ListOfficers: %v", err)
	}
	if len(officers) != 2 {
		t.Fatalf("expected 2 officers, got %d", len(officers))
	}
	if officers[0].Username != "so1" || officers[0].CertThumbprint != "tp1" ||
		officers[0].ShareIndex != 1 || officers[0].AuthFactor != "piv" ||
		!officers[0].EnrolledAt.Equal(enrolled) || !officers[0].SharePending {
		t.Fatalf("unexpected first officer: %+v", officers[0])
	}
	if officers[1].Username != "so2" || officers[1].ShareIndex != 2 {
		t.Fatalf("unexpected second officer: %+v", officers[1])
	}
}

func TestOfficerBinding_ListOfficers_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		officerListFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.ListOfficers(); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OfficerClaimStatus
// ---------------------------------------------------------------------------

func TestOfficerBinding_OfficerClaimStatus_Success(t *testing.T) {
	claimed := time.Date(2026, 6, 2, 8, 30, 0, 0, time.UTC)
	svc := &mockBarrierService{
		officerClaimStatusFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return []*service.BarrierOfficerStatus{
				{Username: "so1", ShareIndex: 1, ClaimedAt: claimed, SharePending: false},
			}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	officers, err := b.OfficerClaimStatus()
	if err != nil {
		t.Fatalf("OfficerClaimStatus: %v", err)
	}
	if len(officers) != 1 {
		t.Fatalf("expected 1 officer, got %d", len(officers))
	}
	if !officers[0].ClaimedAt.Equal(claimed) || officers[0].SharePending {
		t.Fatalf("unexpected claim status: %+v", officers[0])
	}
}

func TestOfficerBinding_OfficerClaimStatus_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		officerClaimStatusFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.OfficerClaimStatus(); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ClaimBegin
// ---------------------------------------------------------------------------

func TestOfficerBinding_ClaimBegin_Success(t *testing.T) {
	expires := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	var gotUser string
	svc := &mockBarrierService{
		shareClaimBeginFn: func(_ context.Context, username, _ string) (*service.BarrierShareClaimChallenge, error) {
			gotUser = username
			return &service.BarrierShareClaimChallenge{NonceHex: "abcd1234", ExpiresAt: expires}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.ClaimBegin(&transport.ShareClaimBeginRequest{Username: "so1"})
	if err != nil {
		t.Fatalf("ClaimBegin: %v", err)
	}
	if gotUser != "so1" {
		t.Fatalf("expected username so1, got %q", gotUser)
	}
	if resp.NonceHex != "abcd1234" || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("unexpected challenge: %+v", resp)
	}
}

func TestOfficerBinding_ClaimBegin_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		shareClaimBeginFn: func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.ClaimBegin(&transport.ShareClaimBeginRequest{Username: "so1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ClaimComplete
// ---------------------------------------------------------------------------

func TestOfficerBinding_ClaimComplete_Success(t *testing.T) {
	var gotUser, gotNonce string
	var gotSig []byte
	svc := &mockBarrierService{
		shareClaimCompleteFn: func(_ context.Context, username, nonceHex string, signature []byte, _ string) (*service.BarrierShareClaimResult, error) {
			gotUser, gotNonce, gotSig = username, nonceHex, signature
			return &service.BarrierShareClaimResult{ShareIndex: 2, ShareBytes: []byte{0x01, 0x02}}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.ClaimComplete(&transport.ShareClaimCompleteRequest{
		Username:  "so1",
		NonceHex:  "abcd",
		Signature: []byte{0xff},
	})
	if err != nil {
		t.Fatalf("ClaimComplete: %v", err)
	}
	if gotUser != "so1" || gotNonce != "abcd" || !bytes.Equal(gotSig, []byte{0xff}) {
		t.Fatalf("unexpected forwarded claim: %q %q %v", gotUser, gotNonce, gotSig)
	}
	if resp.ShareIndex != 2 || !bytes.Equal(resp.ShareBytes, []byte{0x01, 0x02}) {
		t.Fatalf("unexpected claim result: %+v", resp)
	}
}

func TestOfficerBinding_ClaimComplete_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		shareClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.ClaimComplete(&transport.ShareClaimCompleteRequest{Username: "so1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RegisterRecoveryOfficer
// ---------------------------------------------------------------------------

func TestOfficerBinding_RegisterRecoveryOfficer_Success(t *testing.T) {
	var gotReq *service.BarrierOfficerRegisterRequest
	svc := &mockBarrierService{
		recoveryOfficerRegisterFn: func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			gotReq = req
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("REC-CERT"), ShareIndex: 5}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.RegisterRecoveryOfficer(&OfficerRegisterParams{Username: "rec1", CSRPEM: "REC-CSR"})
	if err != nil {
		t.Fatalf("RegisterRecoveryOfficer: %v", err)
	}
	if gotReq.Username != "rec1" || !bytes.Equal(gotReq.CSRPEM, []byte("REC-CSR")) {
		t.Fatalf("unexpected forwarded request: %+v", gotReq)
	}
	if resp.CertPEM != "REC-CERT" || resp.ShareIndex != 5 {
		t.Fatalf("unexpected result: %+v", resp)
	}
}

func TestOfficerBinding_RegisterRecoveryOfficer_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		recoveryOfficerRegisterFn: func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.RegisterRecoveryOfficer(&OfficerRegisterParams{Username: "rec1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RecoveryClaimBegin
// ---------------------------------------------------------------------------

func TestOfficerBinding_RecoveryClaimBegin_Success(t *testing.T) {
	expires := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	var gotUser string
	svc := &mockBarrierService{
		recoveryClaimBeginFn: func(_ context.Context, username, _ string) (*service.BarrierShareClaimChallenge, error) {
			gotUser = username
			return &service.BarrierShareClaimChallenge{NonceHex: "feed", ExpiresAt: expires}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.RecoveryClaimBegin(&transport.ShareClaimBeginRequest{Username: "rec1"})
	if err != nil {
		t.Fatalf("RecoveryClaimBegin: %v", err)
	}
	if gotUser != "rec1" || resp.NonceHex != "feed" || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("unexpected challenge: user=%q resp=%+v", gotUser, resp)
	}
}

func TestOfficerBinding_RecoveryClaimBegin_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		recoveryClaimBeginFn: func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.RecoveryClaimBegin(&transport.ShareClaimBeginRequest{Username: "rec1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RecoveryClaimComplete
// ---------------------------------------------------------------------------

func TestOfficerBinding_RecoveryClaimComplete_Success(t *testing.T) {
	svc := &mockBarrierService{
		recoveryClaimCompleteFn: func(_ context.Context, username, nonceHex string, signature []byte, _ string) (*service.BarrierShareClaimResult, error) {
			if username != "rec1" || nonceHex != "beef" || !bytes.Equal(signature, []byte{0xaa}) {
				t.Fatalf("unexpected forwarded claim: %q %q %v", username, nonceHex, signature)
			}
			return &service.BarrierShareClaimResult{ShareIndex: 4, ShareBytes: []byte{0x09}}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.RecoveryClaimComplete(&transport.ShareClaimCompleteRequest{
		Username:  "rec1",
		NonceHex:  "beef",
		Signature: []byte{0xaa},
	})
	if err != nil {
		t.Fatalf("RecoveryClaimComplete: %v", err)
	}
	if resp.ShareIndex != 4 || !bytes.Equal(resp.ShareBytes, []byte{0x09}) {
		t.Fatalf("unexpected claim result: %+v", resp)
	}
}

func TestOfficerBinding_RecoveryClaimComplete_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		recoveryClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.RecoveryClaimComplete(&transport.ShareClaimCompleteRequest{Username: "rec1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SubmitSealVote
// ---------------------------------------------------------------------------

func TestOfficerBinding_SubmitSealVote_Success(t *testing.T) {
	var gotShare string
	svc := &mockBarrierService{
		shamirSealVoteFn: func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
			gotShare = share
			return &service.UnsealShareProgress{Required: 3, Submitted: 2, Complete: false}, nil
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})

	resp, err := b.SubmitSealVote(&transport.ShamirSealVoteRequest{Share: "share-b64"})
	if err != nil {
		t.Fatalf("SubmitSealVote: %v", err)
	}
	if gotShare != "share-b64" {
		t.Fatalf("expected share forwarded, got %q", gotShare)
	}
	if resp.Required != 3 || resp.Submitted != 2 || resp.Complete {
		t.Fatalf("unexpected progress: %+v", resp)
	}
}

func TestOfficerBinding_SubmitSealVote_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		shamirSealVoteFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, svc, &mockTenantService{})
	if _, err := b.SubmitSealVote(&transport.ShamirSealVoteRequest{Share: "s"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListTenantOfficers
// ---------------------------------------------------------------------------

func TestOfficerBinding_ListTenantOfficers_Success(t *testing.T) {
	enrolled := time.Date(2026, 6, 4, 7, 0, 0, 0, time.UTC)
	var gotTenant string
	svc := &mockTenantService{
		officerListFn: func(_ context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
			gotTenant = tenantID
			return []*ceremony.OfficerStatus{
				{Username: "tso1", CertThumbprint: "ttp1", ShareIndex: 1, AuthFactor: "piv", EnrolledAt: enrolled, SharePending: true},
			}, nil
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)

	officers, err := b.ListTenantOfficers("acme")
	if err != nil {
		t.Fatalf("ListTenantOfficers: %v", err)
	}
	if gotTenant != "acme" {
		t.Fatalf("expected tenant acme, got %q", gotTenant)
	}
	if len(officers) != 1 || officers[0].Username != "tso1" || officers[0].CertThumbprint != "ttp1" ||
		!officers[0].EnrolledAt.Equal(enrolled) || !officers[0].SharePending {
		t.Fatalf("unexpected officers: %+v", officers)
	}
}

func TestOfficerBinding_ListTenantOfficers_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		officerListFn: func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)
	if _, err := b.ListTenantOfficers("acme"); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantOfficerClaimStatus
// ---------------------------------------------------------------------------

func TestOfficerBinding_TenantOfficerClaimStatus_Success(t *testing.T) {
	claimed := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	var gotTenant string
	svc := &mockTenantService{
		officerClaimStatusFn: func(_ context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
			gotTenant = tenantID
			return []*ceremony.OfficerStatus{
				{Username: "tso1", ShareIndex: 2, ClaimedAt: claimed},
			}, nil
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)

	officers, err := b.TenantOfficerClaimStatus("acme")
	if err != nil {
		t.Fatalf("TenantOfficerClaimStatus: %v", err)
	}
	if gotTenant != "acme" || len(officers) != 1 || !officers[0].ClaimedAt.Equal(claimed) {
		t.Fatalf("unexpected claim status: tenant=%q officers=%+v", gotTenant, officers)
	}
}

func TestOfficerBinding_TenantOfficerClaimStatus_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		officerClaimStatusFn: func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)
	if _, err := b.TenantOfficerClaimStatus("acme"); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RegisterTenantOfficer
// ---------------------------------------------------------------------------

func TestOfficerBinding_RegisterTenantOfficer_Success(t *testing.T) {
	var gotTenant string
	var gotReq *ceremony.RegisterOfficerRequest
	svc := &mockTenantService{
		officerRegisterFn: func(_ context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
			gotTenant, gotReq = tenantID, req
			return &ceremony.RegisterOfficerResult{CertPEM: []byte("T-CERT"), ShareIndex: 7}, nil
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)

	resp, err := b.RegisterTenantOfficer(&TenantOfficerRegisterParams{
		TenantID:          "acme",
		Username:          "tso1",
		CSRPEM:            "T-CSR",
		AuthenticatorType: "software",
		AuthenticatorRef:  "ref-1",
	})
	if err != nil {
		t.Fatalf("RegisterTenantOfficer: %v", err)
	}
	if gotTenant != "acme" || gotReq.Username != "tso1" {
		t.Fatalf("unexpected forwarded identity: %q %q", gotTenant, gotReq.Username)
	}
	// The PEM string from the Wails DTO must arrive at the service as bytes.
	if !bytes.Equal(gotReq.CSRPEM, []byte("T-CSR")) {
		t.Fatalf("expected CSR PEM bytes, got %q", gotReq.CSRPEM)
	}
	if gotReq.AuthenticatorType != ceremony.AuthenticatorType("software") || gotReq.AuthenticatorRef != "ref-1" {
		t.Fatalf("expected authenticator fields forwarded, got %q/%q", gotReq.AuthenticatorType, gotReq.AuthenticatorRef)
	}
	if resp.CertPEM != "T-CERT" || resp.ShareIndex != 7 {
		t.Fatalf("unexpected result: %+v", resp)
	}
}

func TestOfficerBinding_RegisterTenantOfficer_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		officerRegisterFn: func(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)
	if _, err := b.RegisterTenantOfficer(&TenantOfficerRegisterParams{TenantID: "acme", Username: "tso1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantClaimBegin
// ---------------------------------------------------------------------------

func TestOfficerBinding_TenantClaimBegin_Success(t *testing.T) {
	expires := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)
	var gotTenant, gotUser string
	svc := &mockTenantService{
		shareClaimBeginFn: func(_ context.Context, tenantID, username, _ string) (*ceremony.ChallengeResponse, error) {
			gotTenant, gotUser = tenantID, username
			return &ceremony.ChallengeResponse{Nonce: []byte{0xde, 0xad}, ExpiresAt: expires}, nil
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)

	resp, err := b.TenantClaimBegin(&transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "tso1"})
	if err != nil {
		t.Fatalf("TenantClaimBegin: %v", err)
	}
	if gotTenant != "acme" || gotUser != "tso1" {
		t.Fatalf("unexpected forwarded identity: %q %q", gotTenant, gotUser)
	}
	// The ceremony nonce bytes are hex-encoded for the wire shape.
	if resp.NonceHex != "dead" || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("unexpected challenge: %+v", resp)
	}
}

func TestOfficerBinding_TenantClaimBegin_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		shareClaimBeginFn: func(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)
	if _, err := b.TenantClaimBegin(&transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "tso1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantClaimComplete
// ---------------------------------------------------------------------------

func TestOfficerBinding_TenantClaimComplete_Success(t *testing.T) {
	var gotTenant, gotUser, gotNonce string
	var gotSig []byte
	svc := &mockTenantService{
		shareClaimCompleteFn: func(_ context.Context, tenantID, username, nonceHex string, signature []byte, _ string) (*ceremony.ShareClaimResult, error) {
			gotTenant, gotUser, gotNonce, gotSig = tenantID, username, nonceHex, signature
			return &ceremony.ShareClaimResult{ShareIndex: 6, ShareBytes: []byte{0x42}}, nil
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)

	resp, err := b.TenantClaimComplete(&transport.TenantShareClaimCompleteRequest{
		TenantID:  "acme",
		Username:  "tso1",
		NonceHex:  "cafe",
		Signature: []byte{0x11},
	})
	if err != nil {
		t.Fatalf("TenantClaimComplete: %v", err)
	}
	if gotTenant != "acme" || gotUser != "tso1" || gotNonce != "cafe" || !bytes.Equal(gotSig, []byte{0x11}) {
		t.Fatalf("unexpected forwarded claim: %q %q %q %v", gotTenant, gotUser, gotNonce, gotSig)
	}
	if resp.ShareIndex != 6 || !bytes.Equal(resp.ShareBytes, []byte{0x42}) {
		t.Fatalf("unexpected claim result: %+v", resp)
	}
}

func TestOfficerBinding_TenantClaimComplete_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		shareClaimCompleteFn: func(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)
	if _, err := b.TenantClaimComplete(&transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "tso1"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SubmitTenantSealVote
// ---------------------------------------------------------------------------

func TestOfficerBinding_SubmitTenantSealVote_Success(t *testing.T) {
	var gotTenant, gotShare string
	svc := &mockTenantService{
		shamirSealVoteFn: func(_ context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error) {
			gotTenant, gotShare = tenantID, share
			return &tenant.TenantBarrierUnsealProgress{TenantID: tenantID, Required: 2, Submitted: 2, Complete: true}, nil
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)

	resp, err := b.SubmitTenantSealVote(&transport.TenantShamirSealVoteRequest{TenantID: "acme", Share: "t-share"})
	if err != nil {
		t.Fatalf("SubmitTenantSealVote: %v", err)
	}
	if gotTenant != "acme" || gotShare != "t-share" {
		t.Fatalf("unexpected forwarded vote: %q %q", gotTenant, gotShare)
	}
	if resp.Required != 2 || resp.Submitted != 2 || !resp.Complete {
		t.Fatalf("unexpected progress: %+v", resp)
	}
}

func TestOfficerBinding_SubmitTenantSealVote_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		shamirSealVoteFn: func(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
			return nil, errOfficerTest
		},
	}
	b := newTestOfficerBinding(t, &mockBarrierService{}, svc)
	if _, err := b.SubmitTenantSealVote(&transport.TenantShamirSealVoteRequest{TenantID: "acme", Share: "s"}); !errors.Is(err, errOfficerTest) {
		t.Fatalf("expected errOfficerTest, got %v", err)
	}
}

func (m *mockTenantService) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
