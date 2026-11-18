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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestPendingAdminCertStore_DeleteRemovesTheRecord(t *testing.T) {
	n := newBareStandaloneNode(t)
	store := PendingAdminCertStore(n.Services().KV)
	ctx := context.Background()
	if err := store.Put(ctx, "admin", []byte("pem")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := store.Get(ctx, "admin"); err != nil || string(got) != "pem" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := store.Delete(ctx, "admin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var notFound *service.KeyNotFoundError
	if got, err := store.Get(ctx, "admin"); !errors.As(err, &notFound) {
		t.Fatalf("Get after Delete = %q, %v; want *service.KeyNotFoundError", got, err)
	}
}

// TestPendingAdminCertStore_SystemSORecordIsHardConfined pins the raw keys
// the store composes for the two first-boot handoff records to the literals
// pkg/service hard-confines on the embedded transport: a drift between them
// would leave a record forgeable through embedded KV (PR #1579 review round
// 2, P0). The store itself, on the raw KV, still reads the record back.
func TestPendingAdminCertStore_SystemSORecordIsHardConfined(t *testing.T) {
	n := newBareStandaloneNode(t)
	store := PendingAdminCertStore(n.Services().KV)
	ctx := context.Background()
	for name, raw := range map[string]string{
		firstboot.PendingSystemSOKey:  "system/pending-system-so",
		firstboot.PendingAdminCertKey: "system/pending-admin-cert",
	} {
		rec := []byte(`{"username":"so"}`)
		if err := store.Put(ctx, name, rec); err != nil {
			t.Fatalf("Put(%q): %v", name, err)
		}
		if got, err := n.Services().KV.Get(ctx, raw); err != nil || string(got) != string(rec) {
			t.Fatalf("raw Get(%q) = %q, %v; want the record the store wrote", raw, got, err)
		}
		confined := service.NewHardConfinedKV(n.Services().KV)
		var confErr *service.HardConfinedKeyError
		if _, err := confined.Get(ctx, raw); !errors.As(err, &confErr) {
			t.Fatalf("hard-confined Get(%q) = %v; want *service.HardConfinedKeyError", raw, err)
		}
		if err := confined.Put(ctx, raw, []byte("forged")); !errors.As(err, &confErr) {
			t.Fatalf("hard-confined Put(%q) = %v; want *service.HardConfinedKeyError", raw, err)
		}
		if got, err := store.Get(ctx, name); err != nil || string(got) != string(rec) {
			t.Fatalf("store Get(%q) = %q, %v; want the record", name, got, err)
		}
	}
}
