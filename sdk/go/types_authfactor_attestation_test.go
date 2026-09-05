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

package sdk

import (
	"context"
	"errors"
	"testing"
)

// newSDKUserAuthFactorService builds the production user auth-factor service
// over an in-memory backend using ONLY names this package exports -- the
// shape a composer that honours the sdk/go boundary can write.
func newSDKUserAuthFactorService(t *testing.T) UserAuthFactorServicer {
	t.Helper()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV: NewBackendKVService(NewStandaloneBackend(NewMemoryStorage())),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

// TestAttestationRecorder_TrustedHostRecordsVerifiedAttestation is the
// go-qrdb#342 contract as a consumer sees it: the servicer the host already
// holds asserts to AttestationRecorder, a VerifiedAttestation is recorded
// under the exported key names, and every symbol involved is an sdk one.
func TestAttestationRecorder_TrustedHostRecordsVerifiedAttestation(t *testing.T) {
	svc := newSDKUserAuthFactorService(t)
	rec, ok := svc.(AttestationRecorder)
	if !ok {
		t.Fatalf("%T does not implement AttestationRecorder", svc)
	}

	ctx := context.Background()
	err := rec.AddAttested(ctx, 7, &AuthFactor{
		Type:       AuthFactorFIDO2,
		Credential: []byte("credential-id"),
		Metadata:   map[string]string{"transports": "usb"},
	}, VerifiedAttestation{Level: AttestationLevelHardware, AAGUID: "ee882879"})
	if err != nil {
		t.Fatalf("AddAttested: %v", err)
	}

	got, err := svc.LookupByCredentialKey(ctx, AuthFactorFIDO2, []byte("credential-id"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	if got.Metadata[MetadataAttestLevel] != string(AttestationLevelHardware) {
		t.Errorf("attest_level = %q, want hardware", got.Metadata[MetadataAttestLevel])
	}
	if got.Metadata[MetadataAttestAAGUID] != "ee882879" {
		t.Errorf("attest_aaguid = %q", got.Metadata[MetadataAttestAAGUID])
	}
	if got.Metadata["transports"] != "usb" {
		t.Errorf("ordinary metadata lost: %v", got.Metadata)
	}
}

// TestAttestationRecorder_EndUserWriteIsStillRefusedAndStampedNone is the
// #234 protection restated through the SDK surface: Add with an attest_* key
// is refused with the exported error, and Add without one is recorded as
// Unattested by the server.
func TestAttestationRecorder_EndUserWriteIsStillRefusedAndStampedNone(t *testing.T) {
	svc := newSDKUserAuthFactorService(t)
	ctx := context.Background()

	err := svc.Add(ctx, 7, &AuthFactor{
		Type:       AuthFactorFIDO2,
		Credential: []byte("forged"),
		Metadata:   map[string]string{MetadataAttestLevel: string(AttestationLevelHardware)},
	})
	var reserved *ReservedMetadataKeyError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %v (%T), want *ReservedMetadataKeyError", err, err)
	}
	if !errors.Is(err, ErrReservedMetadata) {
		t.Error("errors.Is(err, ErrReservedMetadata) is false")
	}
	if reserved.Prefix != ReservedMetadataPrefix {
		t.Errorf("Prefix = %q, want %q", reserved.Prefix, ReservedMetadataPrefix)
	}

	if err := svc.Add(ctx, 7, &AuthFactor{Type: AuthFactorFIDO2, Credential: []byte("plain")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := svc.LookupByCredentialKey(ctx, AuthFactorFIDO2, []byte("plain"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	if got.Metadata[MetadataAttestLevel] != string(Unattested().Level) {
		t.Errorf("attest_level = %q, want %q", got.Metadata[MetadataAttestLevel], Unattested().Level)
	}
}

// TestAttestationRecorder_RefusesUndefinedLevel: the exported error pair for
// a bad level.
func TestAttestationRecorder_RefusesUndefinedLevel(t *testing.T) {
	rec := newSDKUserAuthFactorService(t).(AttestationRecorder)
	err := rec.AddAttested(context.Background(), 7,
		&AuthFactor{Type: AuthFactorFIDO2, Credential: []byte("c")},
		VerifiedAttestation{Level: "hardwre"})
	var invalid *InvalidAttestationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v (%T), want *InvalidAttestationError", err, err)
	}
	if !errors.Is(err, ErrInvalidAttestation) {
		t.Error("errors.Is(err, ErrInvalidAttestation) is false")
	}
}
