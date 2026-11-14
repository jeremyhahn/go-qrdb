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

package user

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// The reaper runs with no caller identity; its expiry events must still
// carry the affected tenant, or a tenant-filtered audit query misses them.
func TestPendingReaper_ExpiryAuditCarriesTenant(t *testing.T) {
	trail := audit.NewRingBuffer(64)
	svc, err := NewStore(newMemKVStore(), newTestIDGen(t), WithAuditLogger(trail))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	acme := createPending(t, svc, "tess", "acme", time.Minute)
	globex := createPending(t, svc, "uma", "globex", time.Minute)

	clock := &fakeClock{now: time.Now().UTC().Add(time.Hour)}
	r, err := NewPendingReaper(PendingReaperConfig{Users: svc, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	for _, tenantID := range []string{"acme", "globex"} {
		if n, err := r.Sweep(ctx, tenantID); err != nil || n != 1 {
			t.Fatalf("sweep %s = %d, %v; want 1, nil", tenantID, n, err)
		}
	}

	for tenantID, want := range map[string]uint64{"acme": acme.ID, "globex": globex.ID} {
		page, err := trail.Query(ctx, audit.Filter{TenantID: tenantID, Action: AuditActionPendingExpired})
		if err != nil {
			t.Fatalf("Query(%s): %v", tenantID, err)
		}
		if len(page.Events) != 1 {
			t.Fatalf("tenant %s expiry events = %d, want 1: %+v", tenantID, len(page.Events), page.Events)
		}
		ev := page.Events[0]
		if ev.TenantID != tenantID || ev.Resource != "user:"+strconv.FormatUint(want, 10) {
			t.Fatalf("tenant %s event = tenant %q resource %q, want user %d", tenantID, ev.TenantID, ev.Resource, want)
		}
	}
}
