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

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func newFirstSetSOAuth(t *testing.T, store kvstore.KVStore) *auth.SOAuthenticator {
	t.Helper()
	a, err := auth.NewSOAuthenticator(store, &soNoopAudit{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	return a
}

// TestSetSOPIN_ConcurrentEnrolmentHasOneWinner is the first-set race (#1129):
// N enrolments of different PINs for one tenant at once. Exactly one may be
// told it set the PIN; every other gets ErrSOPINAlreadySet, and the stored
// record verifies the winner's PIN and no other.
func TestSetSOPIN_ConcurrentEnrolmentHasOneWinner(t *testing.T) {
	const n = 4
	ctx := context.Background()
	a := newFirstSetSOAuth(t, kvstore.NewMemoryKVStore())

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make([]error, n)
	)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = a.SetSOPIN(ctx, "tenant-1", fmt.Appendf(nil, "pin-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range results {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatalf("enrolments %d and %d both succeeded", winner, i)
			}
			winner = i
		case !errors.Is(err, auth.ErrSOPINAlreadySet):
			t.Fatalf("enrolment %d = %v, want nil or ErrSOPINAlreadySet", i, err)
		}
	}
	if winner < 0 {
		t.Fatal("no enrolment succeeded")
	}
	for i := range n {
		err := a.VerifySOPIN(ctx, "tenant-1", fmt.Appendf(nil, "pin-%d", i))
		if i == winner && err != nil {
			t.Fatalf("the winner's PIN does not verify: %v", err)
		}
		if i != winner && err == nil {
			t.Fatalf("loser %d's PIN verifies; the stored record is not the winner's", i)
		}
	}
}

// TestCommitSOPINRecord_ConcurrentCommitHasOneWinner is the same race on the
// replicated-record path, which runs no KDF and so races tighter.
func TestCommitSOPINRecord_ConcurrentCommitHasOneWinner(t *testing.T) {
	const n = 8
	ctx := context.Background()
	a := newFirstSetSOAuth(t, kvstore.NewMemoryKVStore())

	records := make([]string, n)
	for i := range n {
		r, err := auth.HashSOPIN(fmt.Appendf(nil, "pin-%d", i))
		if err != nil {
			t.Fatalf("HashSOPIN: %v", err)
		}
		records[i] = r
	}
	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make([]error, n)
	)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = a.CommitSOPINRecord(ctx, "tenant-1", records[i])
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range results {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatalf("commits %d and %d both succeeded", winner, i)
			}
			winner = i
		case !errors.Is(err, auth.ErrSOPINAlreadySet):
			t.Fatalf("commit %d = %v, want nil or ErrSOPINAlreadySet", i, err)
		}
	}
	if winner < 0 {
		t.Fatal("no commit succeeded")
	}
	if err := a.VerifySOPIN(ctx, "tenant-1", fmt.Appendf(nil, "pin-%d", winner)); err != nil {
		t.Fatalf("the winner's PIN does not verify: %v", err)
	}
}

// plainKV hides the atomic methods of the store it wraps.
type plainKV struct{ kvstore.KVStore }

// TestSetSOPIN_NonAtomicStoreIsRefused: a store that cannot create
// conditionally is refused, not degraded to the racy read-then-write.
func TestSetSOPIN_NonAtomicStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	inner := kvstore.NewMemoryKVStore()
	a := newFirstSetSOAuth(t, plainKV{inner})

	err := a.SetSOPIN(ctx, "tenant-1", []byte("pin"))
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("SetSOPIN over a non-atomic store = %v, want ErrAtomicWriteUnsupported", err)
	}
	if v, _ := inner.Get(ctx, "__sys:so-pin-commit/tenant-1"); v != nil {
		t.Fatal("a refused enrolment wrote a record")
	}
}

// interleavingKV lands a competing write between RotateSOPIN's verification
// and its compare-and-swap, as a concurrent rotation would.
type interleavingKV struct {
	*kvstore.MemoryKVStore
	competing []byte
}

func (s *interleavingKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.competing != nil {
		if err := s.Put(ctx, key, s.competing); err != nil {
			return false, err
		}
		s.competing = nil
	}
	return s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

// TestRotateSOPIN_ConcurrentChangeIsNotOverwritten: a rotation whose verified
// record was replaced before its write gets ErrSOPINChanged and leaves the
// later PIN in place.
func TestRotateSOPIN_ConcurrentChangeIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	store := &interleavingKV{MemoryKVStore: kvstore.NewMemoryKVStore()}
	a := newFirstSetSOAuth(t, store)
	if err := a.SetSOPIN(ctx, "tenant-1", []byte("old-pin")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	later, err := auth.HashSOPIN([]byte("later-pin"))
	if err != nil {
		t.Fatalf("HashSOPIN: %v", err)
	}
	store.competing = []byte(later)

	err = a.RotateSOPIN(ctx, "tenant-1", []byte("old-pin"), []byte("stale-pin"))
	if !errors.Is(err, auth.ErrSOPINChanged) {
		t.Fatalf("RotateSOPIN = %v, want ErrSOPINChanged", err)
	}
	if err := a.VerifySOPIN(ctx, "tenant-1", []byte("later-pin")); err != nil {
		t.Fatalf("the later PIN was overwritten: %v", err)
	}
}
