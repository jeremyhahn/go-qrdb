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

package standalone

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The raw keys of the ceremony -> first-boot handoff records, named as
// wire-level strings so a drift in the guard's own constants fails here.
const (
	pendingSystemSORawKey  = "system/pending-system-so"
	pendingAdminCertRawKey = "system/pending-admin-cert"
)

// TestEmbedded_PendingSystemSORecordUnreachable: the pending system SO
// record (and the pending admin certificate record beside it) lives on the
// same shard the embedded transport serves. An embedded caller holding
// kv:put must not read, forge or delete it, and a wide-open MVCC range must
// not surface it (PR #1579 review round 2, P0).
func TestEmbedded_PendingSystemSORecordUnreachable(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	raw := service.NewKVService(srv.localBackend, serverDefaultShardID)
	seed := []byte(`{"username":"so"}`)
	for _, key := range []string{pendingSystemSORawKey, pendingAdminCertRawKey} {
		if err := raw.Put(ctx, key, seed); err != nil {
			t.Fatalf("seeding %q directly: %v", key, err)
		}
	}

	client := srv.Client()
	for _, key := range []string{pendingSystemSORawKey, pendingAdminCertRawKey} {
		var confErr *service.HardConfinedKeyError
		if _, err := client.KV().Get(ctx, key); !errors.As(err, &confErr) {
			t.Fatalf("KV().Get(%q) error = %v (%T), want *service.HardConfinedKeyError", key, err, err)
		}
		if err := client.KV().Put(ctx, key, []byte(`{"username":"forged"}`)); !errors.As(err, &confErr) {
			t.Fatalf("KV().Put(%q) error = %v (%T), want *service.HardConfinedKeyError", key, err, err)
		}
		if err := client.KV().Delete(ctx, key); !errors.As(err, &confErr) {
			t.Fatalf("KV().Delete(%q) error = %v (%T), want *service.HardConfinedKeyError", key, err, err)
		}
	}

	resp, err := client.MVCC().Range(ctx, []byte(""), []byte("\xff"), transport.RangeOptions{})
	if err != nil {
		t.Fatalf(`MVCC().Range("", "\xff"): %v`, err)
	}
	for _, kv := range resp.Kvs {
		if k := string(kv.Key); k == pendingSystemSORawKey || k == pendingAdminCertRawKey {
			t.Fatalf("MVCC().Range returned the handoff record %q", k)
		}
	}

	for _, key := range []string{pendingSystemSORawKey, pendingAdminCertRawKey} {
		if got, err := raw.Get(ctx, key); err != nil || string(got) != string(seed) {
			t.Fatalf("raw readback of %q = %q, %v; want the seed unchanged", key, got, err)
		}
	}
}
