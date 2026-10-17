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

package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// budgetRecordingUserServicer records how long the context it was handed had
// left to run, so a test can tell which lookup budget lookupUser applied.
type budgetRecordingUserServicer struct {
	*mockUserServicer
	budget time.Duration
}

var _ user.UserServicer = (*budgetRecordingUserServicer)(nil)

func (s *budgetRecordingUserServicer) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.budget = time.Until(deadline)
	}
	return s.mockUserServicer.GetUserByUsername(ctx, tenantID, username)
}

// TestLookupUser_NoFallbackGetsTheFullBudget is the CI failure behind the
// MCP replica-forwarding flake: a tenant the fallback does not hold had its
// linearizable lookup cut at the sub-second fail-over budget with nothing to
// fail over to, so a ReadIndex slower than that under load answered every
// authenticated request with a 503 it did not need to.
func TestLookupUser_NoFallbackGetsTheFullBudget(t *testing.T) {
	for name, m := range map[string]func(*budgetRecordingUserServicer) *AuthMiddleware{
		"no fallback configured": func(p *budgetRecordingUserServicer) *AuthMiddleware {
			return &AuthMiddleware{users: p}
		},
		"fallback holds another tenant": func(p *budgetRecordingUserServicer) *AuthMiddleware {
			return &AuthMiddleware{users: p, usersFallback: &mockUserServicer{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			primary := &budgetRecordingUserServicer{mockUserServicer: &mockUserServicer{err: errors.New("quicraft: timeout")}}
			if _, err := m(primary).lookupUser(context.Background(), "t1", userKey{username: "alice"}); err == nil {
				t.Fatal("expected the primary's error")
			}
			if primary.budget <= authLookupSyncTimeout {
				t.Fatalf("lookup budget = %v, want more than the %v fail-over budget: nothing can answer in its place",
					primary.budget, authLookupSyncTimeout)
			}
			if attempt := service.DefaultSettlePolicy.Attempt; primary.budget > attempt {
				t.Fatalf("lookup budget = %v, want at most the %v attempt bound", primary.budget, attempt)
			}
		})
	}
}

// TestLookupUser_FallbackKeepsTheFailOverBudget pins the other half: where
// the fallback can answer, the linearizable read is still cut short so a blip
// fails over quickly instead of stalling the request.
func TestLookupUser_FallbackKeepsTheFailOverBudget(t *testing.T) {
	primary := &budgetRecordingUserServicer{mockUserServicer: &mockUserServicer{err: errors.New("quicraft: timeout")}}
	fallback := &budgetRecordingUserServicer{mockUserServicer: &mockUserServicer{
		user: &user.User{Username: "alice", TenantID: tenantid.System, Lifecycle: user.LifecycleActive},
	}}
	m := &AuthMiddleware{users: primary, usersFallback: fallback}

	if _, err := m.lookupUser(context.Background(), tenantid.System, userKey{username: "alice"}); err != nil {
		t.Fatalf("lookupUser: %v", err)
	}
	if primary.budget <= 0 || primary.budget > authLookupSyncTimeout {
		t.Fatalf("primary budget = %v, want at most %v", primary.budget, authLookupSyncTimeout)
	}
	if fallback.budget <= authLookupSyncTimeout || fallback.budget > authFallbackTimeout {
		t.Fatalf("fallback budget = %v, want in (%v, %v]", fallback.budget, authLookupSyncTimeout, authFallbackTimeout)
	}
}
