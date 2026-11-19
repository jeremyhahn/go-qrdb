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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// failTenantIndexKV fails the first tenant-index delete with an ordinary
// backend error, the failure the reaper must be able to retry past.
type failTenantIndexKV struct {
	*memKVStore
	armed bool
}

var errTenantIndexDelete = errors.New("tenant index delete failed")

func (s *failTenantIndexKV) Delete(ctx context.Context, key string) error {
	if s.armed && strings.HasPrefix(key, keyPrefixTenant) {
		s.armed = false
		return errTenantIndexDelete
	}
	return s.memKVStore.Delete(ctx, key)
}

// A direct DeleteExpiredPending of an expired account no sweep has claimed
// establishes the durable expiry claim before it mutates the account, so a
// delete that fails part way stays reapable: the claim is listed, and the
// next sweep finishes the delete and audits the reap.
func TestDeleteExpiredPending_UnclaimedFailureThenRetry(t *testing.T) {
	kv := &failTenantIndexKV{memKVStore: newMemKVStore()}
	al := &fakeAuditLogger{}
	svc, err := NewStore(kv, newTestIDGen(t), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	later := time.Now().UTC().Add(time.Hour)
	u := createPending(t, svc, "unclaimed", "acme", time.Minute)
	exp := svc.(PendingExpirer)

	kv.armed = true
	if _, err := exp.DeleteExpiredPending(ctx, u.ID, later); !errors.Is(err, errTenantIndexDelete) {
		t.Fatalf("DeleteExpiredPending err = %v, want the tenant index failure", err)
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser after the failed delete: %v", err)
	}
	if !ExpiryClaimed(got) || !PendingReapable(got, later) {
		t.Fatalf("failed delete left an unclaimed record: lifecycle %v claim %v", got.Lifecycle, got.ExpiryClaim)
	}
	if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("ListExpiryClaims = %v, %v; want [%d]", ids, err, u.ID)
	}

	clock := &fakeClock{now: later}
	r, err := NewPendingReaper(PendingReaperConfig{
		Users:   svc,
		Factors: &fakeFactors{byUser: map[uint64][]*auth.AuthFactor{}},
		Now:     clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("retrying sweep = %d, %v; want 1, nil", n, err)
	}
	var nf *UserNotFoundError
	if _, err := svc.GetUser(ctx, u.ID); !errors.As(err, &nf) {
		t.Fatalf("account survived the retrying sweep: %v", err)
	}
	if n := countAudits(al, AuditActionPendingReaped); n != 1 {
		t.Fatalf("pending_reaped audits = %d, want 1", n)
	}
	if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("reap marker left after the retry finished: %v, %v", ids, err)
	}
}
