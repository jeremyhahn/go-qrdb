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

// What TenantForSerial answers, and why RevokeCert alone could not.
//
// RevokeCert takes a serial, and a serial carries no tenant. TenantForSerial is
// the seam that turns "revoke this serial" into a question about a TARGET, so
// an authorization layer has something to check the caller against.

package cert

import (
	"context"
	"errors"
	"testing"
)

// newTenantSerialProvider returns an initialized provider over a real
// in-memory store and a real AES-GCM barrier.
func newTenantSerialProvider(t *testing.T) *DefaultCertProvider {
	t.Helper()
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier())
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return p
}

// TestTenantForSerial_ResolvesTheIssuingTenant is the case that makes an
// authorization decision possible: a serial this node issued names the tenant
// it was issued to.
func TestTenantForSerial_ResolvesTheIssuingTenant(t *testing.T) {
	p := newTenantSerialProvider(t)
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	tenantID, found, err := p.TenantForSerial(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("TenantForSerial: %v", err)
	}
	if !found {
		t.Fatal("found = false for a serial this provider issued")
	}
	if tenantID != "acme" {
		t.Errorf("tenantID = %q, want %q", tenantID, "acme")
	}
}

// TestTenantForSerial_DistinguishesTwoTenantsSharingAStore is the property the
// cross-tenant gate rests on. One store holds both tenants' records, so an
// implementation that merely reported "some record exists" would satisfy the
// test above and still admit every cross-tenant revocation.
func TestTenantForSerial_DistinguishesTwoTenantsSharingAStore(t *testing.T) {
	p := newTenantSerialProvider(t)
	ctx := context.Background()

	a, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "a-user", TenantID: "acme"})
	if err != nil {
		t.Fatalf("issue acme: %v", err)
	}
	b, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "b-user", TenantID: "globex"})
	if err != nil {
		t.Fatalf("issue globex: %v", err)
	}

	for _, tc := range []struct{ serial, want string }{
		{a.Serial, "acme"},
		{b.Serial, "globex"},
	} {
		got, found, err := p.TenantForSerial(ctx, tc.serial)
		if err != nil {
			t.Fatalf("TenantForSerial(%s): %v", tc.serial, err)
		}
		if !found || got != tc.want {
			t.Errorf("TenantForSerial(%s) = (%q, %v), want (%q, true)", tc.serial, got, found, tc.want)
		}
	}
}

// TestTenantForSerial_UnknownSerialIsNotAnError pins the deny-by-serial
// signal. An unknown serial is a legitimate CA input (go-qrdb#287), so it
// reports found == false rather than failing -- the caller needs to tell "no
// owner" apart from "could not look".
func TestTenantForSerial_UnknownSerialIsNotAnError(t *testing.T) {
	p := newTenantSerialProvider(t)

	tenantID, found, err := p.TenantForSerial(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("an unknown serial must not be an error: %v", err)
	}
	if found {
		t.Error("found = true for a serial no record exists for")
	}
	if tenantID != "" {
		t.Errorf("tenantID = %q, want empty", tenantID)
	}
}

// TestTenantForSerial_EmptySerialIsRejected keeps a blank serial from matching
// whatever the scan happens to reach first.
func TestTenantForSerial_EmptySerialIsRejected(t *testing.T) {
	p := newTenantSerialProvider(t)

	_, _, err := p.TenantForSerial(context.Background(), "")
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if verr.Field != "serial" {
		t.Errorf("Field = %q, want %q", verr.Field, "serial")
	}
}

// TestTenantForSerial_SealedBarrierRefuses keeps a sealed node from answering
// a tenancy question it cannot substantiate. An authorization gate that read
// "no owner" from a sealed node would route every revocation to the
// deny-by-serial branch.
func TestTenantForSerial_SealedBarrierRefuses(t *testing.T) {
	barrier := newMockBarrier()
	p := NewDefaultProvider(newMockKVStore(), barrier)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	barrier.seal()

	_, found, err := p.TenantForSerial(context.Background(), "abc")
	var serr *BarrierSealedError
	if !errors.As(err, &serr) {
		t.Fatalf("err = %v, want *BarrierSealedError", err)
	}
	if found {
		t.Error("found = true from a sealed node")
	}
}

// TestTenantForSerial_StoreFailureIsReported is the other half: a scan that
// could not run must not be reported as an absent owner.
func TestTenantForSerial_StoreFailureIsReported(t *testing.T) {
	sentinel := errors.New("store is down")
	p := NewDefaultProvider(&errorKVStore{err: sentinel}, newMockBarrier())

	_, found, err := p.TenantForSerial(context.Background(), "abc")
	var serr *StoreError
	if !errors.As(err, &serr) {
		t.Fatalf("err = %v, want *StoreError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("the underlying store error must survive wrapping, got %v", err)
	}
	if found {
		t.Error("found = true from a store that answered nothing")
	}
}

// TestTenantForSerial_AgreesWithRevokeCert is the anti-drift test. Both read
// the same records through findBySerial; if that ever stops being true, an
// authorization decision made on one and an effect produced by the other stop
// describing the same certificate.
func TestTenantForSerial_AgreesWithRevokeCert(t *testing.T) {
	p := newTenantSerialProvider(t)
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	_, found, err := p.TenantForSerial(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("TenantForSerial: %v", err)
	}

	result, err := p.RevokeCert(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	if found != result.RecordFound() {
		t.Fatalf("TenantForSerial found = %v but RevokeCert recordFound = %v; the gate and the effect disagree about the same serial",
			found, result.RecordFound())
	}
}
