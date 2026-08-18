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
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// tornKV wraps memKV and fails every Put whose key carries one of the given
// prefixes, modelling a registration that dies partway through persist. The
// KV store has no transaction, so persist's three writes — primary, tenant
// index, username index — are independently droppable.
type tornKV struct {
	*memKV
	mu               sync.Mutex
	failPrefix       []string
	failDeletePrefix []string
}

func newTornKV(failPrefix ...string) *tornKV {
	return &tornKV{memKV: newMemKV(), failPrefix: failPrefix}
}

// failDeletes makes the compensating deletes fail too, modelling a store that
// goes away mid-rollback and strands what the registration wrote.
func (t *tornKV) failDeletes(prefixes ...string) *tornKV {
	t.mu.Lock()
	t.failDeletePrefix = prefixes
	t.mu.Unlock()
	return t
}

func (t *tornKV) Delete(ctx context.Context, key string) error {
	t.mu.Lock()
	prefixes := append([]string(nil), t.failDeletePrefix...)
	t.mu.Unlock()
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return errors.New("simulated delete failure for " + key)
		}
	}
	return t.memKV.Delete(ctx, key)
}

func (t *tornKV) Put(ctx context.Context, key string, value []byte) error {
	t.mu.Lock()
	prefixes := append([]string(nil), t.failPrefix...)
	t.mu.Unlock()
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return errors.New("simulated write failure for " + key)
		}
	}
	return t.memKV.Put(ctx, key, value)
}

// heal stops failing writes, so a retry can proceed against whatever the
// torn registration left behind.
func (t *tornKV) heal() {
	t.mu.Lock()
	t.failPrefix = nil
	t.failDeletePrefix = nil
	t.mu.Unlock()
}

func newTornService(t *testing.T, store *tornKV) custodian.Servicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// enrolmentOf decodes the custodian record stored under id.
func enrolmentOf(t *testing.T, store *tornKV, id string) custodian.Custodian {
	t.Helper()
	data, err := store.Get(context.Background(), "__sys:custodian/"+id)
	if err != nil || data == nil {
		t.Fatalf("no primary record for %s (err %v)", id, err)
	}
	var c custodian.Custodian
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal custodian: %v", err)
	}
	return c
}

func TestRegisterCustodian_TornUsernameIndex_LeavesNothingBehindAndRetryEnrols(t *testing.T) {
	// persist writes the primary record first and the username index last. If
	// that last write fails, the registration has produced a sealed envelope
	// it never returns — envelopes are handed to the caller, never stored —
	// so a surviving primary record would be an enrolment whose share nobody
	// has, and the duplicate check would honour it forever. Compensate: the
	// failed registration leaves the store as it found it, and a retry
	// enrols normally with fresh material.
	ctx := context.Background()
	store := newTornKV("__sys:custodian-idx/username/")
	svc := newTornService(t, store)

	req, _ := testRegisterRequest(t)
	req.EnrollmentID = "enroll-original"
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("expected the torn username-index write to fail registration")
	}

	// Nothing survived: no primary record, and neither index.
	for _, prefix := range []string{
		"__sys:custodian/",
		"__sys:custodian-idx/tenant/",
		"__sys:custodian-idx/username/",
	} {
		keys, err := store.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List(%s): %v", prefix, err)
		}
		if len(keys) != 0 {
			t.Errorf("%s holds %v after a failed registration, want nothing", prefix, keys)
		}
	}

	// The retry is a normal enrolment and yields a usable envelope.
	store.heal()
	retry, _ := testRegisterRequest(t)
	retry.EnrollmentID = "enroll-retry"
	resp, err := svc.RegisterCustodian(ctx, retry)
	if err != nil {
		t.Fatalf("retry after a rolled-back registration: %v", err)
	}
	if resp.SealedEnvelope == nil || len(resp.SealedEnvelope.Ciphertext) == 0 {
		t.Fatal("retry returned no sealed envelope")
	}
	if got := enrolmentOf(t, store, resp.Custodian.ID); got.EnrollmentID != "enroll-retry" {
		t.Errorf("EnrollmentID = %q, want enroll-retry", got.EnrollmentID)
	}
}

func TestRegisterCustodian_TornTenantIndex_LeavesNothingBehind(t *testing.T) {
	// The middle write fails, so only the primary record was written. It must
	// be compensated for the same reason.
	ctx := context.Background()
	store := newTornKV("__sys:custodian-idx/")
	svc := newTornService(t, store)

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("expected the torn index write to fail registration")
	}

	keys, err := store.List(ctx, "__sys:custodian/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("primary records %v survived a failed registration", keys)
	}

	store.heal()
	retry, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, retry); err != nil {
		t.Fatalf("retry after a rolled-back registration: %v", err)
	}
}

func TestRegisterCustodian_RollbackFailure_ReportsTheStrandedKeysAndStillRefusesARetry(t *testing.T) {
	// The compensation is best-effort: a store that fails the deletes too
	// strands what was written. That case must be named — an operator has
	// keys to remove — and the surviving record must still be treated as an
	// enrolment, because nothing can distinguish it from a completed one and
	// overwriting it would destroy a share holder's record.
	ctx := context.Background()
	store := newTornKV("__sys:custodian-idx/").failDeletes("__sys:custodian/")
	svc := newTornService(t, store)

	req, _ := testRegisterRequest(t)
	req.EnrollmentID = "enroll-original"
	_, err := svc.RegisterCustodian(ctx, req)
	var orphaned *custodian.OrphanedEnrolmentError
	if !errors.As(err, &orphaned) {
		t.Fatalf("err = %v (%T), want *custodian.OrphanedEnrolmentError", err, err)
	}
	if len(orphaned.Keys) != 1 || !strings.HasPrefix(orphaned.Keys[0], "__sys:custodian/") {
		t.Fatalf("stranded keys = %v, want the single primary record", orphaned.Keys)
	}
	if !strings.Contains(orphaned.Error(), orphaned.Keys[0]) {
		t.Errorf("Error() = %q, want it to name the stranded key", orphaned.Error())
	}

	// The stranded record is a duplicate, not something to overwrite.
	store.heal()
	retry, _ := testRegisterRequest(t)
	retry.EnrollmentID = "enroll-retry"
	if _, err := svc.RegisterCustodian(ctx, retry); !errors.Is(err, custodian.ErrCustodianAlreadyExists) {
		t.Fatalf("err = %v, want ErrCustodianAlreadyExists", err)
	}
	id := strings.TrimPrefix(orphaned.Keys[0], "__sys:custodian/")
	if after := enrolmentOf(t, store, id); after.EnrollmentID != "enroll-original" {
		t.Errorf("EnrollmentID = %q, want the stranded enroll-original left untouched", after.EnrollmentID)
	}
}

func TestRegisterCustodian_LegacyRecordWithTornUsernameIndex_IsStillADuplicate(t *testing.T) {
	// The hardest case: a custodian enrolled before the UUIDv5 migration
	// whose username index is also missing. Its ID cannot be re-derived
	// (that would call SHA-1) and the two cheap lookups both miss it, so only
	// the tenant-index scan can find it.
	ctx := context.Background()
	store := newTornKV()
	svc := newTornService(t, store)

	c := custodian.Custodian{
		ID:           legacyCustodianIDAlice,
		Username:     "alice",
		TenantID:     "tenant-1",
		Method:       custodian.FactorMethodPIV,
		EnrollmentID: "enroll-legacy",
		PublicKeyPEM: "legacy-pem",
		ShareIndex:   1,
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Put(ctx, "__sys:custodian/"+legacyCustodianIDAlice, data); err != nil {
		t.Fatalf("put primary: %v", err)
	}
	if err := store.Put(ctx, "__sys:custodian-idx/tenant/tenant-1/"+legacyCustodianIDAlice, []byte(legacyCustodianIDAlice)); err != nil {
		t.Fatalf("put tenant index: %v", err)
	}
	// No username index — that is the torn write.

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); !errors.Is(err, custodian.ErrCustodianAlreadyExists) {
		t.Fatalf("err = %v, want ErrCustodianAlreadyExists", err)
	}

	if after := enrolmentOf(t, store, legacyCustodianIDAlice); after.EnrollmentID != "enroll-legacy" {
		t.Errorf("EnrollmentID = %q, want enroll-legacy", after.EnrollmentID)
	}
}

func TestRegisterCustodian_DistinctUsernameStillEnrolsAfterAScan(t *testing.T) {
	// The scan must not turn into a blanket refusal: a different person in
	// the same tenant still enrols.
	ctx := context.Background()
	store := newTornKV()
	svc := newTornService(t, store)

	first, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, first); err != nil {
		t.Fatalf("RegisterCustodian (alice): %v", err)
	}

	second, _ := testRegisterRequest(t)
	second.Username = "bob"
	second.ShareIndex = 2
	if _, err := svc.RegisterCustodian(ctx, second); err != nil {
		t.Fatalf("RegisterCustodian (bob): %v", err)
	}

	list, err := svc.ListCustodians(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ListCustodians: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("tenant holds %d custodians, want 2", len(list))
	}
}

// custodianIDFromTenantIndex returns the single custodian ID recorded in the
// tenant index, failing the test when there is not exactly one.
func custodianIDFromTenantIndex(t *testing.T, store *tornKV, tenantID string) string {
	t.Helper()
	prefix := "__sys:custodian-idx/tenant/" + tenantID + "/"
	keys, err := store.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("tenant index holds %d entries, want 1", len(keys))
	}
	return keys[0][len(prefix):]
}

func TestRegisterCustodian_CorruptLegacyRecord_RefusesRatherThanEnrolsTwice(t *testing.T) {
	// A legacy primary record that cannot be decoded, with no username index
	// to identify it by. Decoding is how a record's username is learned, so
	// this record cannot be ruled out as alice's existing enrolment —
	// skipping it would enrol a second alice, under a second identifier,
	// holding a second sealed share while the first is still out there.
	ctx := context.Background()
	store := newTornKV()
	svc := newTornService(t, store)

	if err := store.Put(ctx, "__sys:custodian/"+legacyCustodianIDAlice, []byte("{not json")); err != nil {
		t.Fatalf("put corrupt primary: %v", err)
	}
	if err := store.Put(ctx, "__sys:custodian-idx/tenant/tenant-1/"+legacyCustodianIDAlice,
		[]byte(legacyCustodianIDAlice)); err != nil {
		t.Fatalf("put tenant index: %v", err)
	}

	req, _ := testRegisterRequest(t)
	_, err := svc.RegisterCustodian(ctx, req)
	if err == nil {
		t.Fatal("registration proceeded past a record it could not rule out")
	}
	var corrupt *custodian.CorruptRecordError
	if !errors.As(err, &corrupt) {
		t.Fatalf("err = %v (%T), want *custodian.CorruptRecordError", err, err)
	}
	if corrupt.ID != legacyCustodianIDAlice {
		t.Errorf("CorruptRecordError.ID = %s, want %s", corrupt.ID, legacyCustodianIDAlice)
	}

	// Nothing was written: no second enrolment, and the corrupt record is
	// left exactly as found for an operator to repair.
	keys, err := store.List(ctx, "__sys:custodian-idx/tenant/tenant-1/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("tenant index holds %d entries, want the 1 corrupt record only", len(keys))
	}
	data, err := store.Get(ctx, "__sys:custodian/"+legacyCustodianIDAlice)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "{not json" {
		t.Errorf("corrupt record = %q, want it left untouched", string(data))
	}
}

func TestRegisterCustodian_CorruptRecordInAnotherTenant_DoesNotBlockEnrolment(t *testing.T) {
	// The refusal is scoped to the tenant being scanned: corruption in some
	// other tenant is not this registration's problem.
	ctx := context.Background()
	store := newTornKV()
	svc := newTornService(t, store)

	if err := store.Put(ctx, "__sys:custodian/other-tenant-record", []byte("{not json")); err != nil {
		t.Fatalf("put corrupt primary: %v", err)
	}
	if err := store.Put(ctx, "__sys:custodian-idx/tenant/tenant-2/other-tenant-record",
		[]byte("other-tenant-record")); err != nil {
		t.Fatalf("put tenant index: %v", err)
	}

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); err != nil {
		t.Fatalf("RegisterCustodian: %v", err)
	}
}

func TestCorruptRecordError_MessageAndUnwrap(t *testing.T) {
	cause := errors.New("invalid character 'n'")
	err := &custodian.CorruptRecordError{ID: "abc", Err: cause}
	if !strings.Contains(err.Error(), "abc") {
		t.Errorf("Error() = %q, want it to name the record", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("Unwrap() does not expose the decode failure")
	}
}
