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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The pending-user reaper purges through these.
var (
	_ user.FactorPurger = (*userAuthFactorService)(nil)
	_ user.FactorPurger = (*routingUserAuthFactorService)(nil)
)

func newPurger(t *testing.T) (UserAuthFactorServicer, UserAuthFactorPurger) {
	t.Helper()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kvstoretest.NewGuardedMemoryStore(),
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	purger, ok := svc.(UserAuthFactorPurger)
	if !ok {
		t.Fatal("auth factor service does not implement UserAuthFactorPurger")
	}
	return svc, purger
}

// Purge removes a revoked factor and its credential index, which a normal
// Delete still refuses, along with the active ones; another user's factors
// stay. A second purge finds nothing to do.
func TestPurge_RemovesRevokedFactorsAndIndexes(t *testing.T) {
	svc, purger := newPurger(t)
	ctx := context.Background()
	for _, f := range []*auth.AuthFactor{
		mtlsFactor("primary", 1, []byte("k1")),
		fido2Factor("backup", 1, []byte("k2")),
		mtlsFactor("other", 2, []byte("k3")),
	} {
		if err := svc.Add(ctx, f.UserID, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	if err := svc.Remove(ctx, "backup", false); err != nil {
		t.Fatalf("Remove backup: %v", err)
	}
	if err := svc.Delete(ctx, "backup", true); !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("Delete of a revoked factor = %v, want ErrFactorRevoked", err)
	}

	n, err := purger.Purge(ctx, 1)
	if err != nil || n != 2 {
		t.Fatalf("Purge = %d, %v; want 2, nil", n, err)
	}
	if left, err := svc.List(ctx, 1); err != nil || len(left) != 0 {
		t.Fatalf("factors left after purge = %v, %v", left, err)
	}
	for typ, key := range map[auth.AuthFactorType]string{auth.AuthFactorMTLS: "k1", auth.AuthFactorFIDO2: "k2"} {
		if _, err := svc.LookupByCredentialKey(ctx, typ, []byte(key)); !errors.Is(err, auth.ErrFactorNotFound) {
			t.Fatalf("credential %s after purge = %v, want ErrFactorNotFound", key, err)
		}
	}
	if got, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k3")); err != nil || got.UserID != 2 {
		t.Fatalf("other user's factor after purge = %v, %v", got, err)
	}
	if n, err := purger.Purge(ctx, 1); err != nil || n != 0 {
		t.Fatalf("second Purge = %d, %v; want 0, nil", n, err)
	}
}

// Purge seals the user's factor set first, so an enrollment that arrives
// after it began -- the reaper's claim-to-purge window -- is refused rather
// than leaving a factor behind for a deleted account.
func TestPurge_SealRefusesLaterEnrollment(t *testing.T) {
	svc, purger := newPurger(t)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("first", 7, []byte("k7"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := purger.Purge(ctx, 7); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	err := svc.Add(ctx, 7, mtlsFactor("late", 7, []byte("k8")))
	if !errors.Is(err, ErrFactorSetSealed) {
		t.Fatalf("enrollment after purge = %v, want ErrFactorSetSealed", err)
	}
	if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k8")); !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("refused enrollment left its credential index: %v", err)
	}

	// A user that never enrolled is sealed too.
	if _, err := purger.Purge(ctx, 8); err != nil {
		t.Fatalf("Purge of a user without factors: %v", err)
	}
	if err := svc.Add(ctx, 8, mtlsFactor("fresh", 8, []byte("k9"))); !errors.Is(err, ErrFactorSetSealed) {
		t.Fatalf("first enrollment after purge = %v, want ErrFactorSetSealed", err)
	}
}
