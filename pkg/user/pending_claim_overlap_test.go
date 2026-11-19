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
	"strings"
	"testing"
	"time"
)

// overlapKV runs other once, at the first reap marker write: before the
// write lands when beforeWrite is set, else right after it.
type overlapKV struct {
	*memKVStore
	beforeWrite bool
	other       func()
}

func (s *overlapKV) Put(ctx context.Context, key string, value []byte) error {
	other := s.other
	if other == nil || !strings.HasPrefix(key, keyPrefixReap) {
		return s.memKVStore.Put(ctx, key, value)
	}
	s.other = nil
	if s.beforeWrite {
		other()
		return s.memKVStore.Put(ctx, key, value)
	}
	if err := s.memKVStore.Put(ctx, key, value); err != nil {
		return err
	}
	other()
	return nil
}

// Two overlapping expiry claims of one account: the second runs whole inside
// the first, between its read of the record and its claim, with the first
// claim's marker write landing either before or after the whole of the
// second. The first loses the record; whichever way they interleave, exactly
// the winner's recovery marker is left, and it finishes the reap.
func TestClaimExpiredPending_OverlappingClaimsKeepTheWinnersMarker(t *testing.T) {
	for name, beforeWrite := range map[string]bool{
		"loser marker lands after the winner":  true,
		"loser marker lands before the winner": false,
	} {
		t.Run(name, func(t *testing.T) {
			kv := &overlapKV{memKVStore: newMemKVStore(), beforeWrite: beforeWrite}
			al := &fakeAuditLogger{}
			svc, err := NewStore(kv, newTestIDGen(t), WithAuditLogger(al))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			s := svc.(*kvUserStore)
			ctx := context.Background()
			now := time.Now().UTC().Add(time.Hour)
			u := createPending(t, svc, "contested", "acme", time.Minute)

			var winner *User
			var winErr error
			kv.other = func() { winner, winErr = s.ClaimExpiredPending(ctx, u.ID, now) }
			if _, err := s.ClaimExpiredPending(ctx, u.ID, now); err == nil {
				t.Fatal("the overlapped claim won the record too")
			}
			if winErr != nil || winner == nil || !ExpiryClaimed(winner) {
				t.Fatalf("overlapping claim = %v, %v; want the claimed account", winner, winErr)
			}
			marker, err := s.readReapMarker(ctx, u.ID)
			if err != nil || marker == nil {
				t.Fatalf("winner's reap marker after the overlap = %v, %v; want it kept", marker, err)
			}
			if marker.ID != u.ID || marker.ExpiryClaim == nil || !marker.ExpiryClaim.Equal(*winner.ExpiryClaim) {
				t.Fatalf("reap marker = %+v, want the winner's claim of %d", marker, u.ID)
			}
			if ids, err := s.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 1 || ids[0] != u.ID {
				t.Fatalf("ListExpiryClaims = %v, %v; want [%d]", ids, err, u.ID)
			}

			if _, err := s.DeleteExpiredPending(ctx, u.ID, now); err != nil {
				t.Fatalf("DeleteExpiredPending: %v", err)
			}
			if got := countAudits(al, "pending_reaped"); got != 1 {
				t.Fatalf("pending_reaped audits = %d, want 1", got)
			}
			if ids, err := s.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
				t.Fatalf("reap marker left after the reap: %v, %v", ids, err)
			}
		})
	}
}
