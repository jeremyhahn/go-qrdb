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
	"slices"
	"testing"
)

// errGuardRefused is the refusal the guards below return.
var errGuardRefused = errors.New("guard refused")

// TestUpdateUser_GuardJudgesTheCommittedRecord: the guard sees the record as
// committed and the record as the request writes it, and its refusal is
// returned unchanged with nothing written.
func TestUpdateUser_GuardJudgesTheCommittedRecord(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	u := createTestUser(t, svc, "guarded", "acme")
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, RoleIDs: []string{"role-a"}}); err != nil {
		t.Fatalf("seed roles: %v", err)
	}

	var gotPrior, gotNext *User
	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		RoleIDs:      []string{"role-b"},
		RoleBindings: []RoleBinding{{RoleID: "role-c", TenantID: "acme"}},
		Attributes:   map[string]string{"k": "v"},
		Guard: func(prior, next *User) error {
			gotPrior, gotNext = prior, next
			return errGuardRefused
		},
	})
	if !errors.Is(err, errGuardRefused) {
		t.Fatalf("want the guard's refusal, got %v", err)
	}
	if !slices.Equal(gotPrior.RoleIDs, []string{"role-a"}) || gotPrior.RoleBindings != nil {
		t.Fatalf("prior = %+v, want the committed record", gotPrior)
	}
	if !slices.Equal(gotNext.RoleIDs, []string{"role-b"}) || len(gotNext.RoleBindings) != 1 || gotNext.Attributes["k"] != "v" {
		t.Fatalf("next = %+v, want the record as written", gotNext)
	}

	stored, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !slices.Equal(stored.RoleIDs, []string{"role-a"}) || stored.Attributes != nil {
		t.Fatalf("a refused update wrote %+v", stored)
	}
}

// TestUpdateUser_GuardCannotBeOutrun: a write that commits between the guard
// and the conditional commit fails the guarded update rather than being
// overwritten by it, so a guard's decision holds for the version written.
func TestUpdateUser_GuardCannotBeOutrun(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	u := createTestUser(t, svc, "raced", "acme")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      u.ID,
		RoleIDs: []string{"role-a"},
		Guard: func(prior, _ *User) error {
			if slices.Contains(prior.RoleIDs, "role-so") {
				return errGuardRefused
			}
			// The concurrent write lands after the guard passed.
			_, werr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, RoleIDs: []string{"role-so"}})
			return werr
		},
	})
	var lost *ConcurrentUpdateError
	if !errors.As(err, &lost) {
		t.Fatalf("want *ConcurrentUpdateError, got %T: %v", err, err)
	}
	stored, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !slices.Equal(stored.RoleIDs, []string{"role-so"}) {
		t.Fatalf("roles = %v, want the concurrent write's", stored.RoleIDs)
	}
}

// TestUpdateUser_GuardPassingCommits: a guard that passes leaves the update
// to commit as it would without one.
func TestUpdateUser_GuardPassingCommits(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)
	u := createTestUser(t, svc, "passes", "acme")
	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      u.ID,
		RoleIDs: []string{"role-a"},
		Guard:   func(_, _ *User) error { return nil },
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if !slices.Equal(updated.RoleIDs, []string{"role-a"}) {
		t.Fatalf("roles = %v, want [role-a]", updated.RoleIDs)
	}
}
