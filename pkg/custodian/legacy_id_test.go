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

package custodian_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// legacyCustodianIDAlice is the identifier the pre-FIPS UUIDv5 (name-based,
// SHA-1) derivation produced for tenant "tenant-1", username "alice" —
// uuid.NewSHA1(uuid.NameSpaceDNS, "tenant-1:alice"). It is a frozen literal,
// not recomputed: SHA-1 panics outright under GODEBUG=fips140=only, which is
// the build this test must pass in.
const legacyCustodianIDAlice = "b753d87d-47df-547c-a6db-51fae92894e0"

// seedLegacyCustodian writes a custodian record and both secondary indexes
// exactly as a pre-migration release would have: keyed on the UUIDv5
// identifier. It stands in for an already-provisioned cluster.
func seedLegacyCustodian(t *testing.T, store *memKV, id, tenantID, username string) {
	t.Helper()
	c := custodian.Custodian{
		ID:           id,
		Username:     username,
		TenantID:     tenantID,
		Method:       custodian.FactorMethodPIV,
		EnrollmentID: "enroll-legacy",
		PublicKeyPEM: "legacy-pem",
		ShareIndex:   1,
		EnrolledAt:   time.Now().UTC(),
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal legacy custodian: %v", err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "__sys:custodian/"+id, data); err != nil {
		t.Fatalf("put legacy primary: %v", err)
	}
	if err := store.Put(ctx, "__sys:custodian-idx/tenant/"+tenantID+"/"+id, []byte(id)); err != nil {
		t.Fatalf("put legacy tenant index: %v", err)
	}
	if err := store.Put(ctx, "__sys:custodian-idx/username/"+tenantID+"/"+username, []byte(id)); err != nil {
		t.Fatalf("put legacy username index: %v", err)
	}
}

func TestRegisterCustodian_IDIsUUIDv8(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("RegisterCustodian: %v", err)
	}

	parsed, err := uuid.Parse(resp.Custodian.ID)
	if err != nil {
		t.Fatalf("custodian ID %q is not a parseable UUID: %v", resp.Custodian.ID, err)
	}
	if parsed.Version() != 8 {
		t.Errorf("version = %d, want 8 (RFC 9562 §5.8)", parsed.Version())
	}
	if parsed.Variant() != uuid.RFC4122 {
		t.Errorf("variant = %v, want RFC4122", parsed.Variant())
	}
	if len(resp.Custodian.ID) != 36 {
		t.Errorf("length = %d, want 36 (shape must be unchanged)", len(resp.Custodian.ID))
	}
	if resp.Custodian.ID == legacyCustodianIDAlice {
		t.Errorf("still deriving the legacy UUIDv5 identifier %s", legacyCustodianIDAlice)
	}
}

func TestRegisterCustodian_IDIsDeterministic(t *testing.T) {
	ctx := context.Background()

	svcA, _ := newTestService(t)
	reqA, _ := testRegisterRequest(t)
	respA, err := svcA.RegisterCustodian(ctx, reqA)
	if err != nil {
		t.Fatalf("RegisterCustodian (A): %v", err)
	}

	svcB, _ := newTestService(t)
	reqB, _ := testRegisterRequest(t)
	respB, err := svcB.RegisterCustodian(ctx, reqB)
	if err != nil {
		t.Fatalf("RegisterCustodian (B): %v", err)
	}

	if respA.Custodian.ID != respB.Custodian.ID {
		t.Errorf("same tenant+username derived %s and %s", respA.Custodian.ID, respB.Custodian.ID)
	}

	svcC, _ := newTestService(t)
	reqC, _ := testRegisterRequest(t)
	reqC.Username = "bob"
	respC, err := svcC.RegisterCustodian(ctx, reqC)
	if err != nil {
		t.Fatalf("RegisterCustodian (C): %v", err)
	}
	if respC.Custodian.ID == respA.Custodian.ID {
		t.Errorf("alice and bob collide on %s", respA.Custodian.ID)
	}
}

func TestGetCustodian_ResolvesLegacyPersistedIdentifier(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	seedLegacyCustodian(t, store, legacyCustodianIDAlice, "tenant-1", "alice")

	got, err := svc.GetCustodian(ctx, legacyCustodianIDAlice)
	if err != nil {
		t.Fatalf("GetCustodian(%s): %v", legacyCustodianIDAlice, err)
	}
	if got.Username != "alice" {
		t.Errorf("username = %q, want alice", got.Username)
	}

	list, err := svc.ListCustodians(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ListCustodians: %v", err)
	}
	if len(list) != 1 || list[0].ID != legacyCustodianIDAlice {
		t.Errorf("ListCustodians = %+v, want the single legacy custodian %s", list, legacyCustodianIDAlice)
	}

	if err := svc.RevokeCustodian(ctx, legacyCustodianIDAlice); err != nil {
		t.Fatalf("RevokeCustodian: %v", err)
	}
	revoked, err := svc.GetCustodian(ctx, legacyCustodianIDAlice)
	if err != nil {
		t.Fatalf("GetCustodian after revoke: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Error("legacy custodian was not revoked")
	}
}

func TestRegisterCustodian_LegacyEnrolmentIsStillADuplicate(t *testing.T) {
	// The duplicate check reads the username index rather than re-deriving the
	// ID. Without that, alice would be enrolled a second time under the new
	// identifier and hold two shares.
	ctx := context.Background()
	svc, store := newTestService(t)
	seedLegacyCustodian(t, store, legacyCustodianIDAlice, "tenant-1", "alice")

	req, _ := testRegisterRequest(t)
	_, err := svc.RegisterCustodian(ctx, req)
	if !errors.Is(err, custodian.ErrCustodianAlreadyExists) {
		t.Fatalf("err = %v, want ErrCustodianAlreadyExists", err)
	}

	list, err := svc.ListCustodians(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ListCustodians: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("tenant holds %d custodian records, want 1", len(list))
	}
}
