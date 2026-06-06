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

package authfactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/authfactor"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeTransport is a hand-written transport.AuthFactorClient stub used by the
// tests. It records the last call arguments and returns a configurable
// response or error. No mocking framework is used — Go interfaces are
// trivially satisfied.
type fakeTransport struct {
	enrollResp    *authfactor.EnrollFactorResponse
	enrollErr     error
	enrollSeenReq *authfactor.EnrollFactorRequest

	challengeResp    *authfactor.ChallengeFactorResponse
	challengeErr     error
	challengeSeenReq *authfactor.ChallengeFactorRequest

	verifyErr     error
	verifySeenReq *authfactor.VerifyFactorRequest

	listResp    *authfactor.ListFactorsResponse
	listErr     error
	listSeenReq *authfactor.ListFactorsRequest

	revokeErr     error
	revokeSeenReq *authfactor.RevokeFactorRequest
}

var _ transport.AuthFactorClient = (*fakeTransport)(nil)

func (f *fakeTransport) EnrollFactor(_ context.Context, req *authfactor.EnrollFactorRequest) (*authfactor.EnrollFactorResponse, error) {
	f.enrollSeenReq = req
	return f.enrollResp, f.enrollErr
}

func (f *fakeTransport) ChallengeFactor(_ context.Context, req *authfactor.ChallengeFactorRequest) (*authfactor.ChallengeFactorResponse, error) {
	f.challengeSeenReq = req
	return f.challengeResp, f.challengeErr
}

func (f *fakeTransport) VerifyFactor(_ context.Context, req *authfactor.VerifyFactorRequest) error {
	f.verifySeenReq = req
	return f.verifyErr
}

func (f *fakeTransport) ListFactors(_ context.Context, req *authfactor.ListFactorsRequest) (*authfactor.ListFactorsResponse, error) {
	f.listSeenReq = req
	return f.listResp, f.listErr
}

func (f *fakeTransport) RevokeFactor(_ context.Context, req *authfactor.RevokeFactorRequest) error {
	f.revokeSeenReq = req
	return f.revokeErr
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew_NilTransportReturnsTypedError(t *testing.T) {
	t.Parallel()
	c, err := authfactor.New(nil)
	if c != nil {
		t.Fatalf("New(nil): expected nil client, got %#v", c)
	}
	var nte *authfactor.NilTransportError
	if !errors.As(err, &nte) {
		t.Fatalf("New(nil): expected *NilTransportError, got %T (%v)", err, err)
	}
	if nte.Error() == "" {
		t.Fatal("NilTransportError.Error() must be non-empty")
	}
}

func TestNew_WithTransportSucceeds(t *testing.T) {
	t.Parallel()
	c, err := authfactor.New(&fakeTransport{})
	if err != nil {
		t.Fatalf("New(non-nil): unexpected error %v", err)
	}
	if c == nil {
		t.Fatal("New(non-nil): expected non-nil client")
	}
}

// ---------------------------------------------------------------------------
// EnrollFactor
// ---------------------------------------------------------------------------

func TestEnrollFactor_DelegatesToTransport(t *testing.T) {
	t.Parallel()
	want := &authfactor.EnrollFactorResponse{
		EnrollmentID: "enr-1",
		FactorName:   authfactor.FactorFIDO2,
		UserID:       "alice",
		EnrolledAt:   time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
	}
	ft := &fakeTransport{enrollResp: want}
	c, _ := authfactor.New(ft)

	req := &authfactor.EnrollFactorRequest{
		FactorName: authfactor.FactorFIDO2,
		Subject:    "alice",
	}
	got, err := c.EnrollFactor(context.Background(), req)
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if got != want {
		t.Errorf("EnrollFactor: response not propagated unchanged")
	}
	if ft.enrollSeenReq != req {
		t.Errorf("EnrollFactor: transport did not see exact request pointer")
	}
}

func TestEnrollFactor_NilRequestReturnsTypedError(t *testing.T) {
	t.Parallel()
	c, _ := authfactor.New(&fakeTransport{})

	resp, err := c.EnrollFactor(context.Background(), nil)
	if resp != nil {
		t.Errorf("EnrollFactor(nil): expected nil response, got %#v", resp)
	}
	var nre *authfactor.NilRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("EnrollFactor(nil): expected *NilRequestError, got %T (%v)", err, err)
	}
	if nre.Method != "EnrollFactor" {
		t.Errorf("NilRequestError.Method = %q, want %q", nre.Method, "EnrollFactor")
	}
}

func TestEnrollFactor_TransportErrorPropagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("backend exploded")
	c, _ := authfactor.New(&fakeTransport{enrollErr: wantErr})

	_, err := c.EnrollFactor(context.Background(), &authfactor.EnrollFactorRequest{
		FactorName: authfactor.FactorPIV, Subject: "bob",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnrollFactor: expected wrapped %v, got %v", wantErr, err)
	}
}

// ---------------------------------------------------------------------------
// ChallengeFactor
// ---------------------------------------------------------------------------

func TestChallengeFactor_DelegatesToTransport(t *testing.T) {
	t.Parallel()
	want := &authfactor.ChallengeFactorResponse{
		ChallengeID:  "ch-1",
		EnrollmentID: "enr-1",
		Nonce:        []byte{0xde, 0xad, 0xbe, 0xef},
		ExpiresAt:    time.Date(2026, 4, 27, 12, 5, 0, 0, time.UTC),
	}
	ft := &fakeTransport{challengeResp: want}
	c, _ := authfactor.New(ft)

	got, err := c.ChallengeFactor(context.Background(), &authfactor.ChallengeFactorRequest{EnrollmentID: "enr-1"})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if got != want {
		t.Errorf("ChallengeFactor: response not propagated unchanged")
	}
	if ft.challengeSeenReq == nil || ft.challengeSeenReq.EnrollmentID != "enr-1" {
		t.Errorf("ChallengeFactor: transport did not see expected request")
	}
}

func TestChallengeFactor_NilRequestReturnsTypedError(t *testing.T) {
	t.Parallel()
	c, _ := authfactor.New(&fakeTransport{})

	_, err := c.ChallengeFactor(context.Background(), nil)
	var nre *authfactor.NilRequestError
	if !errors.As(err, &nre) || nre.Method != "ChallengeFactor" {
		t.Fatalf("ChallengeFactor(nil): expected *NilRequestError{ChallengeFactor}, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// VerifyFactor
// ---------------------------------------------------------------------------

func TestVerifyFactor_DelegatesToTransport(t *testing.T) {
	t.Parallel()
	ft := &fakeTransport{}
	c, _ := authfactor.New(ft)

	req := &authfactor.VerifyFactorRequest{
		ChallengeID: "ch-1",
		Value:       []byte{0x01, 0x02},
	}
	if err := c.VerifyFactor(context.Background(), req); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
	if ft.verifySeenReq != req {
		t.Errorf("VerifyFactor: transport did not see exact request pointer")
	}
}

func TestVerifyFactor_NilRequestReturnsTypedError(t *testing.T) {
	t.Parallel()
	c, _ := authfactor.New(&fakeTransport{})

	err := c.VerifyFactor(context.Background(), nil)
	var nre *authfactor.NilRequestError
	if !errors.As(err, &nre) || nre.Method != "VerifyFactor" {
		t.Fatalf("VerifyFactor(nil): expected *NilRequestError{VerifyFactor}, got %v", err)
	}
}

func TestVerifyFactor_TransportErrorPropagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("verify failed")
	c, _ := authfactor.New(&fakeTransport{verifyErr: wantErr})

	err := c.VerifyFactor(context.Background(), &authfactor.VerifyFactorRequest{ChallengeID: "ch-1"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("VerifyFactor: expected wrapped %v, got %v", wantErr, err)
	}
}

// ---------------------------------------------------------------------------
// ListFactors
// ---------------------------------------------------------------------------

func TestListFactors_NilRequestUsesEmptyFilter(t *testing.T) {
	t.Parallel()
	want := &authfactor.ListFactorsResponse{Total: 0}
	ft := &fakeTransport{listResp: want}
	c, _ := authfactor.New(ft)

	got, err := c.ListFactors(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListFactors(nil): %v", err)
	}
	if got != want {
		t.Errorf("ListFactors: response not propagated unchanged")
	}
	if ft.listSeenReq == nil {
		t.Fatal("ListFactors(nil): transport must receive non-nil empty request")
	}
	if ft.listSeenReq.Subject != "" {
		t.Errorf("ListFactors(nil): expected empty subject, got %q", ft.listSeenReq.Subject)
	}
}

func TestListFactors_SubjectFilterPropagates(t *testing.T) {
	t.Parallel()
	ft := &fakeTransport{listResp: &authfactor.ListFactorsResponse{}}
	c, _ := authfactor.New(ft)

	if _, err := c.ListFactors(context.Background(), &authfactor.ListFactorsRequest{Subject: "alice"}); err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if ft.listSeenReq.Subject != "alice" {
		t.Errorf("ListFactors: subject filter not propagated, got %q", ft.listSeenReq.Subject)
	}
}

// ---------------------------------------------------------------------------
// RevokeFactor
// ---------------------------------------------------------------------------

func TestRevokeFactor_DelegatesToTransport(t *testing.T) {
	t.Parallel()
	ft := &fakeTransport{}
	c, _ := authfactor.New(ft)

	if err := c.RevokeFactor(context.Background(), &authfactor.RevokeFactorRequest{EnrollmentID: "enr-1"}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
	if ft.revokeSeenReq == nil || ft.revokeSeenReq.EnrollmentID != "enr-1" {
		t.Errorf("RevokeFactor: transport did not see expected request")
	}
}

func TestRevokeFactor_NilRequestReturnsTypedError(t *testing.T) {
	t.Parallel()
	c, _ := authfactor.New(&fakeTransport{})

	err := c.RevokeFactor(context.Background(), nil)
	var nre *authfactor.NilRequestError
	if !errors.As(err, &nre) || nre.Method != "RevokeFactor" {
		t.Fatalf("RevokeFactor(nil): expected *NilRequestError{RevokeFactor}, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Factor name constants
// ---------------------------------------------------------------------------

func TestFactorConstants(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"pkcs11": authfactor.FactorPKCS11,
		"piv":    authfactor.FactorPIV,
		"fido2":  authfactor.FactorFIDO2,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("factor constant: got %q, want %q", got, want)
		}
	}
}
