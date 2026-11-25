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

package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// failingGetKV is a store whose reads fail, the way a store that lost
// quorum fails them.
type failingGetKV struct {
	*soMemKV
}

var errSOStoreRead = errors.New("store read failed")

func (f *failingGetKV) Get(context.Context, string) ([]byte, error) {
	return nil, errSOStoreRead
}

func newSOAuthOn(t *testing.T, store kvstore.KVStore) *auth.SOAuthenticator {
	t.Helper()
	a, err := auth.NewSOAuthenticator(store, &soNoopAudit{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	return a
}

func TestSOPINCommitted_ReportsCommitState(t *testing.T) {
	ctx := context.Background()
	a := newSOAuthOn(t, newSOMemKV())

	if _, err := a.SOPINCommitted(ctx, ""); !errors.Is(err, auth.ErrSOPINEmptyTenant) {
		t.Fatalf("empty tenant: err = %v, want ErrSOPINEmptyTenant", err)
	}
	got, err := a.SOPINCommitted(ctx, "t1")
	if err != nil || got {
		t.Fatalf("before SetSOPIN: (%v, %v), want (false, nil)", got, err)
	}
	if err := a.SetSOPIN(ctx, "t1", []byte("correct horse battery")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	got, err = a.SOPINCommitted(ctx, "t1")
	if err != nil || !got {
		t.Fatalf("after SetSOPIN: (%v, %v), want (true, nil)", got, err)
	}
	if got, _ := a.SOPINCommitted(ctx, "t2"); got {
		t.Fatalf("another tenant reported committed")
	}
}

func TestSOPINReads_StoreErrorIsOperationError(t *testing.T) {
	ctx := context.Background()
	a := newSOAuthOn(t, &failingGetKV{soMemKV: newSOMemKV()})
	record, err := auth.HashSOPIN([]byte("correct horse battery"))
	if err != nil {
		t.Fatalf("HashSOPIN: %v", err)
	}

	checks := map[string]error{}
	_, checks["SOPINCommitted"] = a.SOPINCommitted(ctx, "t1")
	_, checks["SOPINRecordMatches"] = a.SOPINRecordMatches(ctx, "t1", record)
	// CommitSOPINRecord reads nothing: its once-only check is the store's
	// conditional create (TestCommitSOPINRecord_ConcurrentCommitHasOneWinner).

	for name, err := range checks {
		var opErr *auth.OperationError
		if !errors.As(err, &opErr) {
			t.Fatalf("%s: err = %v, want *OperationError", name, err)
		}
		if !errors.Is(err, errSOStoreRead) {
			t.Fatalf("%s: err = %v does not wrap the store error", name, err)
		}
	}
}
