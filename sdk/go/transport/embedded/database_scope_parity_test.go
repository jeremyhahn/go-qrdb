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

package embedded

import (
	"context"
	"errors"
	"testing"
)

// Scoping a transport to a database confines the families it serves; it
// never grants one the unscoped transport withholds. A standalone node's
// embedded transport serves no MVCC, leases or watches, and a scoped clone of
// it must refuse them the same way rather than resolve them through the
// database registry, which a standalone node composes for its SQL catalog.
func TestWithDatabase_DoesNotGrantWithheldFamilies(t *testing.T) {
	reg := &recordingRegistry{}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(reg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scopedTr, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	scoped := scopedTr.(*Transport)
	ctx := context.Background()

	var nilSvc *NilServiceError
	if _, err := scoped.PutWithLease(ctx, "k", []byte("v"), 1); !errors.As(err, &nilSvc) {
		t.Errorf("scoped PutWithLease = %v, want *NilServiceError like the unscoped transport", err)
	}
	if _, err := scoped.LeaseGrant(ctx, 10); !errors.As(err, &nilSvc) {
		t.Errorf("scoped LeaseGrant = %v, want *NilServiceError like the unscoped transport", err)
	}
	if len(reg.asked) != 0 {
		t.Fatalf("a withheld family resolved through the registry: %v", reg.asked)
	}

	// A family the receiver serves is scoped.
	if err := scoped.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("scoped Put: %v", err)
	}
	if len(reg.asked) != 1 || reg.asked[0] != "system/ledger" {
		t.Fatalf("scoped Put resolved %v, want [system/ledger]", reg.asked)
	}
}
