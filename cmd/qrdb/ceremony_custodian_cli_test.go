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

// ceremony_custodian_cli_test.go exercises the factor-verified custodian
// ceremony subcommands (claim-share-begin / claim-share-complete /
// submit-unseal-share) end-to-end through the embedded transport.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// ceremony claim-share-begin
// ---------------------------------------------------------------------------

func TestCeremonyClaimShareBegin_DefaultFormat(t *testing.T) {
	expiry := time.Now().Add(15 * time.Minute)
	svc := &mockCeremonyService{
		shareBeginFn: func(_ context.Context, custodianID string) (*service.CeremonyCustodianChallenge, error) {
			return &service.CeremonyCustodianChallenge{
				CustodianID: custodianID,
				Nonce:       []byte("nonce-bytes"),
				ExpiresAt:   expiry,
			}, nil
		},
	}
	output, err := executeCeremonyCmd(t, svc, "claim-share-begin", "--custodian-id", "officer-1")
	if err != nil {
		t.Fatalf("claim-share-begin error: %v", err)
	}
	for _, want := range []string{"CustodianID: officer-1", "Nonce:", "ExpiresAt:"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCeremonyClaimShareBegin_JSONFormat(t *testing.T) {
	svc := &mockCeremonyService{
		shareBeginFn: func(_ context.Context, custodianID string) (*service.CeremonyCustodianChallenge, error) {
			return &service.CeremonyCustodianChallenge{CustodianID: custodianID, Nonce: []byte("n"), ExpiresAt: time.Now()}, nil
		},
	}
	prevFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prevFormat })

	output, err := executeCeremonyCmd(t, svc, "claim-share-begin", "--custodian-id", "officer-2")
	if err != nil {
		t.Fatalf("claim-share-begin json error: %v", err)
	}
	if !strings.Contains(output, "officer-2") {
		t.Errorf("json output missing custodian id, got: %q", output)
	}
}

func TestCeremonyClaimShareBegin_ServiceError(t *testing.T) {
	svc := &mockCeremonyService{
		shareBeginFn: func(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
			return nil, fmt.Errorf("custodian not enrolled")
		},
	}
	if _, err := executeCeremonyCmd(t, svc, "claim-share-begin", "--custodian-id", "ghost"); err == nil {
		t.Fatal("expected error from service, got nil")
	}
}

// ---------------------------------------------------------------------------
// ceremony claim-share-complete
// ---------------------------------------------------------------------------

func TestCeremonyClaimShareComplete_DefaultFormat(t *testing.T) {
	svc := &mockCeremonyService{
		shareCompleteFn: func(_ context.Context, custodianID, _ string, _ []byte) (*custodian.Custodian, error) {
			return &custodian.Custodian{
				ID:           custodianID,
				Username:     "alice",
				ShareIndex:   3,
				ShareClaimed: true,
			}, nil
		},
	}
	output, err := executeCeremonyCmd(t, svc,
		"claim-share-complete",
		"--custodian-id", "officer-1",
		"--challenge-id", "chal-1",
		"--factor-response", "AQID", // base64 of {1,2,3}
	)
	if err != nil {
		t.Fatalf("claim-share-complete error: %v", err)
	}
	for _, want := range []string{"ID: officer-1", "Username: alice", "ShareIndex: 3", "ShareClaimed: true"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCeremonyClaimShareComplete_InvalidFactorBase64(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc,
		"claim-share-complete",
		"--custodian-id", "officer-1",
		"--challenge-id", "chal-1",
		"--factor-response", "!!!not-base64!!!",
	)
	if err == nil {
		t.Fatal("expected base64 decode error, got nil")
	}
	var decErr *Base64DecodeError
	if !errors.As(err, &decErr) {
		t.Errorf("expected *Base64DecodeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ceremony submit-unseal-share
// ---------------------------------------------------------------------------

func TestCeremonySubmitUnsealShare_DefaultFormat(t *testing.T) {
	var gotShare []byte
	svc := &mockCeremonyService{
		submitUnsealFn: func(_ context.Context, _, _ string, _, shareBytes []byte) (*service.CeremonyUnsealShareResult, error) {
			gotShare = shareBytes
			return &service.CeremonyUnsealShareResult{Submitted: 2, Required: 3, Unsealed: false}, nil
		},
	}
	output, err := executeCeremonyCmd(t, svc,
		"submit-unseal-share",
		"--custodian-id", "officer-1",
		"--share", "AQID", // base64 of {1,2,3}
	)
	if err != nil {
		t.Fatalf("submit-unseal-share error: %v", err)
	}
	for _, want := range []string{"Submitted: 2", "Required: 3", "Unsealed: false"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
	if string(gotShare) != string([]byte{1, 2, 3}) {
		t.Errorf("share bytes not decoded correctly, got: %v", gotShare)
	}
}

func TestCeremonySubmitUnsealShare_InvalidShareBase64(t *testing.T) {
	svc := &mockCeremonyService{}
	_, err := executeCeremonyCmd(t, svc,
		"submit-unseal-share",
		"--custodian-id", "officer-1",
		"--share", "@@@",
	)
	if err == nil {
		t.Fatal("expected base64 decode error, got nil")
	}
}

func TestCeremonySubmitUnsealShare_Unsealed(t *testing.T) {
	svc := &mockCeremonyService{
		submitUnsealFn: func(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
			return &service.CeremonyUnsealShareResult{Submitted: 3, Required: 3, Unsealed: true}, nil
		},
	}
	output, err := executeCeremonyCmd(t, svc,
		"submit-unseal-share",
		"--custodian-id", "officer-3",
		"--challenge-id", "chal-3",
		"--factor-response", "AQID",
		"--share", "BAUG",
	)
	if err != nil {
		t.Fatalf("submit-unseal-share error: %v", err)
	}
	if !strings.Contains(output, "Unsealed: true") {
		t.Errorf("expected Unsealed: true, got: %q", output)
	}
}
