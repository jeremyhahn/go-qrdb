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

package cert

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// RevokeCert result reporting -- go-qrdb#287 defect 1: RevokeCert could not
// report what it did. These tests are the verification the issue asks for:
// revoking a serial this node issued and one it does not hold a record for
// must be distinguishable to the caller, and the marker must exist in both
// cases.
//
// BREAKING (go-qrdb#287): RevokeCert now returns (*RevokeResult, error)
// instead of error alone.
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_RevokeCert_RecordUpdated(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	bundle, err := generateForTest(ctx, p, &testRequest{Username: "revoke-result-user", TenantID: "system"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	result, err := p.RevokeCert(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if result.Serial != bundle.Serial {
		t.Errorf("Serial = %q, want %q", result.Serial, bundle.Serial)
	}
	if result.Outcome != RevokeOutcomeRecordUpdated {
		t.Errorf("Outcome = %v, want RevokeOutcomeRecordUpdated", result.Outcome)
	}
	if !result.RecordFound() {
		t.Error("RecordFound() should be true when this node issued the certificate")
	}

	// The marker is durable regardless of outcome.
	revoked, err := p.IsRevoked(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Error("marker should exist after RevokeCert")
	}

	// The display flag on the record was also updated.
	records, err := p.ListCerts(ctx, "system")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	found := false
	for _, rec := range records {
		if rec.Serial == bundle.Serial {
			found = true
			if !rec.Revoked {
				t.Error("CertRecord.Revoked should be true")
			}
		}
	}
	if !found {
		t.Fatal("revoked record not found in ListCerts")
	}
}

func TestDefaultCertProvider_RevokeCert_DenyBySerial(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// No CertRecord was ever created for this serial: it stands in for a
	// certificate issued by a peer, or a mistyped one -- both indistinguishable
	// until this call.
	const serial = "peer-issued-or-mistyped-serial"

	result, err := p.RevokeCert(ctx, serial)
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if result.Outcome != RevokeOutcomeDenyBySerial {
		t.Errorf("Outcome = %v, want RevokeOutcomeDenyBySerial", result.Outcome)
	}
	if result.RecordFound() {
		t.Error("RecordFound() should be false when no local record matched")
	}

	// The marker is durable even though no record matched -- deny-by-serial
	// is a real, successful revocation, not a no-op.
	revoked, err := p.IsRevoked(ctx, serial)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Error("marker should exist for a deny-by-serial revocation")
	}
}

// TestDefaultCertProvider_RevokeCert_DistinguishesOutcomes is the
// verification asked for directly: revoke a serial this node issued and one
// it does not hold a record for, in the same test, and assert the caller can
// tell them apart.
func TestDefaultCertProvider_RevokeCert_DistinguishesOutcomes(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	bundle, err := generateForTest(ctx, p, &testRequest{Username: "distinguish-user", TenantID: "system"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	issued, err := p.RevokeCert(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("RevokeCert(issued): %v", err)
	}
	denied, err := p.RevokeCert(ctx, "no-such-serial")
	if err != nil {
		t.Fatalf("RevokeCert(deny-by-serial): %v", err)
	}

	if issued.Outcome == denied.Outcome {
		t.Fatalf("expected distinct outcomes, both reported %v", issued.Outcome)
	}
	if !issued.RecordFound() {
		t.Error("issued serial should report RecordFound() == true")
	}
	if denied.RecordFound() {
		t.Error("deny-by-serial should report RecordFound() == false")
	}
}

func TestDefaultCertProvider_RevokeCert_EmptySerial_ReturnsResultNil(t *testing.T) {
	p, _, _ := initializeProvider(t)

	result, err := p.RevokeCert(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty serial")
	}
	if result != nil {
		t.Errorf("result should be nil on error, got %+v", result)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_WhenSealed_ReturnsResultNil(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	result, err := p.RevokeCert(context.Background(), "some-serial")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	if result != nil {
		t.Errorf("result should be nil on error, got %+v", result)
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_ScanError_ReturnsResultNil(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier, WithTrustDomain(sanTestDomain))
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	bundle, err := generateForTest(ctx, p, &testRequest{Username: "scan-err-user", TenantID: "system"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	p.kvStore = &partialErrorKVStore{mockKVStore: inner, failScan: true}

	result, err := p.RevokeCert(ctx, bundle.Serial)
	if err == nil {
		t.Fatal("expected error when scan fails")
	}
	if result != nil {
		t.Errorf("result should be nil on error, got %+v", result)
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestRevokeOutcome_String(t *testing.T) {
	tests := map[RevokeOutcome]string{
		RevokeOutcomeRecordUpdated: "record_updated",
		RevokeOutcomeDenyBySerial:  "deny_by_serial",
		RevokeOutcome(99):          "unknown",
	}
	for outcome, want := range tests {
		if got := outcome.String(); got != want {
			t.Errorf("RevokeOutcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

// Note: there is no server-side ExportPKCS12 any more (go-qrdb security
// review: no private key may reach a node to be packaged), so the
// revocation-gated export checks go-qrdb#287 added here no longer apply --
// EncodePKCS12 is a pure, local, no-store encoder with nothing to check
// against. The revocation record itself is still exercised by RevokeCert's
// own tests elsewhere in this package.
