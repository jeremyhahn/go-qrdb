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

// This file carries NO build constraint, and mockKVService — the only helper
// it borrows — is defined in transport_test.go, which carries none either.
// A helper behind a constraint its callers are not a subset of breaks the
// package under every tag set that drops it.

package embedded

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newRealKVService builds the SAME service the embedded server wires into
// newServerKVAdapter (server.go: createServerStorage -> createServerEngine ->
// backend.NewLocalBackend -> service.NewKVService), over the memory backend.
//
// A fake that returns a hand-written KeyNotFoundError would prove only that
// the adapter handles the error the test author remembered. These assertions
// are about the shape production actually emits, so they are driven through
// the real service.
func newRealKVService(t *testing.T) service.KVServicer {
	t.Helper()

	stg, pdb, err := createServerStorage(database.BackendMemory, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("createServerStorage(memory): %v", err)
	}
	t.Cleanup(func() {
		if pdb != nil {
			_ = pdb.Close()
		}
		if stg != nil {
			_ = stg.Close()
		}
	})

	sm, err := createServerEngine(database.BackendMemory, stg, pdb, nil, nil)
	if err != nil {
		t.Fatalf("createServerEngine(memory): %v", err)
	}
	return service.NewKVService(backend.NewLocalBackend(sm), serverDefaultShardID)
}

// TestServerKVAdapter_Exists_AbsentIsFalseNil is the go-qrdb#238 fix. Exists
// returned (false, err) for an absent key, which is the COMMONEST answer to
// the question it is asked: a caller asking "is this key here?" was handed an
// error for "no". kvstore.KVStore's contract is that absence is a value.
func TestServerKVAdapter_Exists_AbsentIsFalseNil(t *testing.T) {
	ctx := context.Background()
	a := newServerKVAdapter(newRealKVService(t))

	// Never written.
	ok, err := a.Exists(ctx, "auth/user/never-written")
	if err != nil {
		t.Fatalf("Exists on a never-written key returned an error: %v\n"+
			"absence is a value on this interface, not a failure", err)
	}
	if ok {
		t.Error("Exists = true for a key that was never written")
	}

	// Written, then deleted — the same absence by a different route.
	const key = "auth/user/alice"
	if err := a.Put(ctx, key, []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ok, err := a.Exists(ctx, key); err != nil || !ok {
		t.Fatalf("Exists after Put = (%v, %v), want (true, nil)", ok, err)
	}
	if err := a.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, err = a.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists after Delete returned an error: %v", err)
	}
	if ok {
		t.Error("Exists = true after Delete")
	}
}

// TestServerKVAdapter_Get_AbsentIsNilNil pins the half that already worked, so
// that moving it off the substring match cannot regress it.
func TestServerKVAdapter_Get_AbsentIsNilNil(t *testing.T) {
	ctx := context.Background()
	a := newServerKVAdapter(newRealKVService(t))

	val, err := a.Get(ctx, "auth/user/never-written")
	if err != nil {
		t.Fatalf("Get on an absent key returned an error: %v", err)
	}
	if val != nil {
		t.Errorf("Get on an absent key = %q, want nil", val)
	}

	const key = "auth/role/admin"
	if err := a.Put(ctx, key, []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if val, err := a.Get(ctx, key); err != nil || string(val) != "payload" {
		t.Fatalf("Get after Put = (%q, %v), want (\"payload\", nil)", val, err)
	}
}

// opaqueAbsenceError wraps a KeyNotFoundError in a message that contains
// NEITHER "not found" NOR "key not found". It is what any rewording of the
// service's error strings would look like to this adapter.
type opaqueAbsenceError struct{ inner error }

func (e *opaqueAbsenceError) Error() string { return "service: read failed (E_ABSENT)" }
func (e *opaqueAbsenceError) Unwrap() error { return e.inner }

// rewordedKVSvc is a KVServicer whose absence error carries no recognisable
// wording — the type is intact and only the text has moved.
type rewordedKVSvc struct{ *mockKVService }

func (m *rewordedKVSvc) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := m.mockKVService.Get(ctx, key)
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			return nil, &opaqueAbsenceError{inner: err}
		}
		return nil, err
	}
	return val, nil
}

// TestServerKVAdapter_AbsenceIsDecidedByType_NotWording is the assertion that
// the substring match is gone rather than merely rewritten.
//
// The old Get worked by coincidence of phrasing: the service wraps not-found
// as (&ReadError{&KeyNotFoundError{}}), which renders as "service: read
// failed: service: key not found: <key>" and happens to contain "not found".
// Nothing pinned that. Any rewording of either error would have turned every
// absent key into a hard error for pkg/user and pkg/role, which read through
// Get and test the result with `if data == nil`.
//
// So this drives the adapter with an absence whose TEXT is deliberately
// hostile and whose TYPE is intact. It fails against the substring
// implementation and passes against errors.As, which is the whole point of the
// change.
func TestServerKVAdapter_AbsenceIsDecidedByType_NotWording(t *testing.T) {
	ctx := context.Background()

	// Assert the premise, so a future edit to opaqueAbsenceError that
	// reintroduced the wording could not quietly make this test vacuous.
	msg := (&opaqueAbsenceError{}).Error()
	if strings.Contains(msg, "not found") {
		t.Fatalf("the fixture error %q contains %q, so this test would pass "+
			"against the substring match it exists to rule out", msg, "not found")
	}

	a := newServerKVAdapter(&rewordedKVSvc{mockKVService: newMockKVService()})

	val, err := a.Get(ctx, "absent")
	if err != nil {
		t.Errorf("Get: absence reported as an error when the wording changed: %v\n"+
			"the type is what decides absence, not the message", err)
	}
	if val != nil {
		t.Errorf("Get = %q, want nil", val)
	}

	ok, err := a.Exists(ctx, "absent")
	if err != nil {
		t.Errorf("Exists: absence reported as an error when the wording changed: %v", err)
	}
	if ok {
		t.Error("Exists = true for an absent key")
	}
}

// TestServerKVAdapter_HandlesTheWrappedShape covers the shape the service
// emits on a linearizable read: ReadError around KeyNotFoundError. errors.As
// unwraps it; a bare type assertion would not.
func TestServerKVAdapter_HandlesTheWrappedShape(t *testing.T) {
	for name, absent := range map[string]error{
		"bare":              &service.KeyNotFoundError{Key: "k"},
		"wrapped in a read": &service.ReadError{Err: &service.KeyNotFoundError{Key: "k"}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			a := newServerKVAdapter(&fixedErrKVSvc{
				mockKVService: newMockKVService(),
				err:           absent,
			})

			if val, err := a.Get(ctx, "k"); err != nil || val != nil {
				t.Errorf("Get = (%q, %v), want (nil, nil)", val, err)
			}
			if ok, err := a.Exists(ctx, "k"); err != nil || ok {
				t.Errorf("Exists = (%v, %v), want (false, nil)", ok, err)
			}
		})
	}
}

// fixedErrKVSvc returns one chosen error from every Get.
type fixedErrKVSvc struct {
	*mockKVService
	err error
}

func (m *fixedErrKVSvc) Get(context.Context, string) ([]byte, error) { return nil, m.err }

// TestServerKVAdapter_NonAbsenceErrorsPropagate is the other half of the
// contract, and the one a too-eager fix would break: an error that is NOT
// absence must still reach the caller from both methods. Reporting a failed
// read as "the key is not there" is the same class of lie in the opposite
// direction.
func TestServerKVAdapter_NonAbsenceErrorsPropagate(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("service: storage unavailable")
	a := newServerKVAdapter(&fixedErrKVSvc{mockKVService: newMockKVService(), err: boom})

	if _, err := a.Get(ctx, "k"); !errors.Is(err, boom) {
		t.Errorf("Get swallowed a non-absence error: got %v, want %v", err, boom)
	}
	ok, err := a.Exists(ctx, "k")
	if !errors.Is(err, boom) {
		t.Errorf("Exists swallowed a non-absence error: got %v, want %v", err, boom)
	}
	if ok {
		t.Error("Exists = true alongside an error")
	}
}
