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
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func newRecordSOAuth(t *testing.T) *auth.SOAuthenticator {
	t.Helper()
	a, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), &soNoopAudit{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A PIN hashed on one node and committed as a record on another verifies
// exactly as one SetSOPIN committed.
func TestCommitSOPINRecord_VerifiesTheHashedPIN(t *testing.T) {
	ctx := context.Background()
	record, err := auth.HashSOPIN([]byte("so-pin-1357"))
	if err != nil {
		t.Fatalf("HashSOPIN: %v", err)
	}
	if strings.Contains(record, "so-pin-1357") {
		t.Fatal("the record carries the PIN")
	}
	a := newRecordSOAuth(t)
	if err := a.CommitSOPINRecord(ctx, "system", record); err != nil {
		t.Fatalf("CommitSOPINRecord: %v", err)
	}
	if err := a.VerifySOPIN(ctx, "system", []byte("so-pin-1357")); err != nil {
		t.Fatalf("VerifySOPIN(right PIN): %v", err)
	}
	if err := a.VerifySOPIN(ctx, "system", []byte("wrong")); err == nil {
		t.Fatal("VerifySOPIN(wrong PIN) = nil")
	}
	if err := a.CommitSOPINRecord(ctx, "system", record); !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("second commit = %v, want ErrSOPINAlreadySet", err)
	}
	if err := a.SetSOPIN(ctx, "system", []byte("other")); !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("SetSOPIN after a record commit = %v, want ErrSOPINAlreadySet", err)
	}
}

func TestCommitSOPINRecord_RefusesMalformedRecords(t *testing.T) {
	good, err := auth.HashSOPIN([]byte("pin"))
	if err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]string{
		"empty":       "",
		"plain PIN":   "so-pin-1357",
		"unknown KDF": "Z" + good[1:],
		"bad hex":     good[:70] + "zz" + good[72:],
		"truncated":   good[:len(good)-1],
		"no colon":    good[:66] + "0" + good[67:],
	} {
		t.Run(name, func(t *testing.T) {
			if err := newRecordSOAuth(t).CommitSOPINRecord(context.Background(), "system", record); !errors.Is(err, auth.ErrSOPINInvalid) {
				t.Fatalf("CommitSOPINRecord(%s) = %v, want ErrSOPINInvalid", name, err)
			}
		})
	}
	if err := newRecordSOAuth(t).CommitSOPINRecord(context.Background(), "", good); !errors.Is(err, auth.ErrSOPINEmptyTenant) {
		t.Fatalf("empty tenant = %v", err)
	}
	if _, err := auth.HashSOPIN(nil); !errors.Is(err, auth.ErrSOPINEmptyPIN) {
		t.Fatalf("HashSOPIN(nil) = %v", err)
	}
}

func TestSOPINRecordMatches_ComparesTheCommittedRecord(t *testing.T) {
	ctx := context.Background()
	record, err := auth.HashSOPIN([]byte("so-pin-1357"))
	if err != nil {
		t.Fatalf("HashSOPIN: %v", err)
	}
	other, err := auth.HashSOPIN([]byte("so-pin-1357"))
	if err != nil {
		t.Fatalf("HashSOPIN: %v", err)
	}
	a := newRecordSOAuth(t)
	if ok, err := a.SOPINRecordMatches(ctx, "system", record); err != nil || ok {
		t.Fatalf("SOPINRecordMatches(nothing committed) = %v, %v; want false", ok, err)
	}
	if err := a.CommitSOPINRecord(ctx, "system", record); err != nil {
		t.Fatalf("CommitSOPINRecord: %v", err)
	}
	if ok, err := a.SOPINRecordMatches(ctx, "system", record); err != nil || !ok {
		t.Fatalf("SOPINRecordMatches(committed record) = %v, %v; want true", ok, err)
	}
	// The same PIN under a fresh salt is a different record: only the
	// record this enrolment committed matches.
	if ok, err := a.SOPINRecordMatches(ctx, "system", other); err != nil || ok {
		t.Fatalf("SOPINRecordMatches(another record) = %v, %v; want false", ok, err)
	}
	if _, err := a.SOPINRecordMatches(ctx, "", record); !errors.Is(err, auth.ErrSOPINEmptyTenant) {
		t.Fatalf("SOPINRecordMatches(empty tenant) = %v, want ErrSOPINEmptyTenant", err)
	}
}
