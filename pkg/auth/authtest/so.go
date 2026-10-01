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

package authtest

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// SOIdentity returns an identity holding the built-in SO role of its own
// tenant, by the identifier BuiltInRoles persists it under, and nothing else.
func SOIdentity(tenantID, subject string) *auth.Identity {
	return &auth.Identity{
		Subject:  subject,
		TenantID: tenantID,
		Roles:    []string{role.SystemRoleID(tenantID, role.RoleNameSO)},
	}
}

// GrantedNonSOIdentity returns an identity that RBAC admits to perms without
// the SO role: it holds no role, and the permissions arrive as
// ScopePermissions, the way a platform token issued for them does. It is the
// caller the SO gate exists to refuse even though authorization lets it
// through.
func GrantedNonSOIdentity(tenantID, subject string, perms ...permission.Permission) *auth.Identity {
	scope := make([]string, 0, len(perms))
	for _, p := range perms {
		scope = append(scope, string(p))
	}
	return &auth.Identity{
		Subject:          subject,
		TenantID:         tenantID,
		ScopePermissions: scope,
	}
}

// SOVerifier returns the real auth.SOAuthenticator over an in-memory store,
// with pin committed for every tenant in tenantIDs and an in-memory audit
// sink.
func SOVerifier(t testing.TB, pin string, tenantIDs ...string) *auth.SOAuthenticator {
	t.Helper()
	v, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("authtest: SO authenticator: %v", err)
	}
	for _, tenantID := range tenantIDs {
		if err := v.SetSOPIN(context.Background(), tenantID, []byte(pin)); err != nil {
			t.Fatalf("authtest: set SO PIN for %q: %v", tenantID, err)
		}
	}
	return v
}

// Barrier is a service.BarrierServicer that implements BarrierRekey and
// BarrierDeleteRecoveryKeys and counts the calls that reached it, so a test
// can tell a refused request from one the service performed. Every other
// method is the nil embedded interface: a test that reaches one has left the
// path it meant to drive.
type Barrier struct {
	service.BarrierServicer
	rekeys  atomic.Int64
	deletes atomic.Int64
}

// BarrierDeleteRecoveryKeys records the deletion.
func (b *Barrier) BarrierDeleteRecoveryKeys(context.Context) error {
	b.deletes.Add(1)
	return nil
}

// RecoveryDeletes reports how many recovery-key deletions reached the service.
func (b *Barrier) RecoveryDeletes() int64 { return b.deletes.Load() }

// BarrierRekey records the rekey.
func (b *Barrier) BarrierRekey(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	b.rekeys.Add(1)
	return &service.ShamirInitInfo{ShareCount: total, Threshold: threshold, TotalShares: total}, nil
}

// Rekeys reports how many rekeys reached the service.
func (b *Barrier) Rekeys() int64 { return b.rekeys.Load() }
