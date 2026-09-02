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

// Embedded-transport half of the go-qrdb#234 cross-transport verification, and
// the one that answers a cross-repo question the issue asked us to confirm.
//
// go-quicbaas#381 assumed its attested-enrollment write-through is safe from
// this rule because it goes through the UserAuthFactorClient TRANSPORT surface
// rather than the servicer. These tests establish that the assumption does not
// hold for the embedded implementation of that surface: it calls the servicer
// directly, so it inherits the refusal like every wire transport does.
package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func realUserFactorService(t *testing.T) service.UserAuthFactorServicer {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{
		KV: newMockKVService(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

// TestEmbedded_UserAuthFactorClientInheritsTheRefusal is the finding, asserted:
// the in-process client surface is NOT a way around the server-owned namespace.
func TestEmbedded_UserAuthFactorClientInheritsTheRefusal(t *testing.T) {
	tr := &Transport{userAuthFactorSvc: realUserFactorService(t)}

	_, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{
		UserID:     42,
		Type:       "mtls",
		Credential: []byte("cred"),
		Metadata:   map[string]string{"attest_level": "hardware"},
	})
	if err == nil {
		t.Fatal("the embedded UserAuthFactorClient accepted a server-owned metadata key")
	}
	var reserved *auth.ReservedMetadataKeyError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %v (%T), want *auth.ReservedMetadataKeyError", err, err)
	}
	if reserved.Key != "attest_level" {
		t.Errorf("error names key %q, want attest_level", reserved.Key)
	}
}

// TestEmbedded_UserAuthFactorRotateInheritsTheRefusal covers the other write.
func TestEmbedded_UserAuthFactorRotateInheritsTheRefusal(t *testing.T) {
	svc := realUserFactorService(t)
	tr := &Transport{userAuthFactorSvc: svc}

	seed := &auth.AuthFactor{
		ID: "old", UserID: 42, Type: auth.AuthFactorMTLS, Credential: []byte("cred-old"),
	}
	if err := svc.Add(context.Background(), 42, seed); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	_, err := tr.RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{
		OldFactorID: "old",
		NewFactor: transport.AuthFactorAddRequest{
			UserID:     42,
			Type:       "mtls",
			Credential: []byte("cred-new"),
			Metadata:   map[string]string{"attest_aaguid": "forged"},
		},
	})
	var reserved *auth.ReservedMetadataKeyError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %v (%T), want *auth.ReservedMetadataKeyError", err, err)
	}
}

// TestEmbedded_AllowsOrdinaryMetadata is the control.
func TestEmbedded_AllowsOrdinaryMetadata(t *testing.T) {
	tr := &Transport{userAuthFactorSvc: realUserFactorService(t)}

	resp, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{
		UserID:     42,
		Type:       "mtls",
		Credential: []byte("cred"),
		Metadata:   map[string]string{"vendor": "yubico"},
	})
	if err != nil {
		t.Fatalf("Add with ordinary metadata: %v", err)
	}
	if resp.Factor.Metadata["vendor"] != "yubico" {
		t.Errorf("ordinary metadata not round-tripped: %v", resp.Factor.Metadata)
	}
}
