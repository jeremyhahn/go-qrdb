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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// hookKV interposes on Put so a test can interleave two registrations at exact
// points. The hook runs before the write and outside any store lock, so it may
// block; returning an error fails the write instead of performing it.
type hookKV struct {
	*memKV
	onPut atomic.Pointer[func(key string, value []byte) error]
}

func newHookKV() *hookKV { return &hookKV{memKV: newMemKV()} }

func (h *hookKV) hook(fn func(key string, value []byte) error) {
	h.onPut.Store(&fn)
}

func (h *hookKV) Put(ctx context.Context, key string, value []byte) error {
	if fn := h.onPut.Load(); fn != nil {
		if err := (*fn)(key, value); err != nil {
			return err
		}
	}
	return h.memKV.Put(ctx, key, value)
}

func newHookService(t *testing.T, store *hookKV) custodian.Servicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestRegisterCustodian_ConcurrentSameUsername_CompensationSparesTheWinner
// drives the interleaving in which compensation is dangerous: duplicate
// detection and the writes are separate operations, so two registrations for
// one tenant and username can both find nothing, and they derive the SAME
// identifier — they write the same three keys. If the one that fails then
// compensates blindly, it deletes the other's completed enrolment.
//
// The schedule below is deterministic, driven by channels rather than timing:
//
//	B: duplicate check passes, then parks before writing anything
//	A: duplicate check passes (B has written nothing), writes primary + tenant index
//	A: parks at its username-index write, which is going to fail
//	B: resumes and completes all three writes, overwriting A's record
//	A: its write fails and it rolls back — and must delete nothing, because
//	   the record it would delete is B's
func TestRegisterCustodian_ConcurrentSameUsername_CompensationSparesTheWinner(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var (
		bParked        = make(chan struct{})
		bMayWrite      = make(chan struct{})
		bDone          = make(chan struct{})
		aAtFailedWrite = make(chan struct{})
		aReachedFinal  atomic.Bool
	)

	store.hook(func(key string, value []byte) error {
		switch {
		case strings.HasPrefix(key, "__sys:custodian/") && bytes.Contains(value, []byte("enroll-B")):
			close(bParked)
			<-bMayWrite
		case strings.HasPrefix(key, "__sys:custodian-idx/username/") && !aReachedFinal.Swap(true):
			// A's last write. Let B finish entirely before failing it, so A's
			// compensation runs against a store B now owns.
			close(aAtFailedWrite)
			<-bDone
			return errors.New("simulated username-index write failure")
		}
		return nil
	})

	type result struct {
		resp *custodian.RegisterResponse
		err  error
	}
	bResult := make(chan result, 1)
	go func() {
		req, _ := testRegisterRequest(t)
		req.EnrollmentID = "enroll-B"
		resp, err := svc.RegisterCustodian(ctx, req)
		bResult <- result{resp, err}
	}()

	<-bParked

	aResult := make(chan result, 1)
	go func() {
		req, _ := testRegisterRequest(t)
		req.EnrollmentID = "enroll-A"
		resp, err := svc.RegisterCustodian(ctx, req)
		aResult <- result{resp, err}
	}()

	<-aAtFailedWrite
	close(bMayWrite)
	b := <-bResult
	close(bDone)
	a := <-aResult

	if b.err != nil {
		t.Fatalf("B reported failure: %v", b.err)
	}
	if a.err == nil {
		t.Fatal("A's failed write did not fail the registration")
	}

	// B reported success, so B's enrolment must be intact and addressable:
	// the record it wrote, and both indexes pointing at it.
	got := enrolmentOfKV(t, store.memKV, b.resp.Custodian.ID)
	if got.EnrollmentID != "enroll-B" {
		t.Errorf("EnrollmentID = %q, want enroll-B — A's rollback destroyed the winner", got.EnrollmentID)
	}
	for _, key := range []string{
		"__sys:custodian-idx/tenant/" + got.TenantID + "/" + got.ID,
		"__sys:custodian-idx/username/" + got.TenantID + "/" + got.Username,
	} {
		v, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
		if len(v) == 0 {
			t.Errorf("%s is missing — A's rollback stripped the winner's index", key)
		}
	}

	// And the enrolment stays addressable through the service.
	if _, err := svc.GetCustodian(ctx, b.resp.Custodian.ID); err != nil {
		t.Errorf("GetCustodian after the race: %v", err)
	}
}

// TestRegisterCustodian_CompensationRunsWhenTheRecordIsStillOurs is the other
// half of the ownership rule: with no competitor, the failed registration must
// still clean up after itself rather than becoming conservative.
func TestRegisterCustodian_CompensationRunsWhenTheRecordIsStillOurs(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	store.hook(func(key string, _ []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/username/") {
			return errors.New("simulated username-index write failure")
		}
		return nil
	})

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("expected the failing write to fail registration")
	}

	keys, err := store.List(ctx, "__sys:custodian/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("primary records %v survived, want the record compensated", keys)
	}
}
