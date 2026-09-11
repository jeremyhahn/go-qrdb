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

// The audit-failure posture, as it is WIRED (go-qrdb#335).
//
// pkg/auth/middleware tests the policy; these test that it survives the trip
// through BuildAuthMiddleware, which is the only constructor both qrdbd and
// the SDK node use. A posture the middleware enforces and the wiring drops is
// a posture nobody has.

package wiring

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// brokenSink is a decision logger over a REAL ChainFileLogger whose file has
// been closed, so the append fails at os.File.Write and the sink returns a
// real *audit.WriteError.
func brokenSink(t *testing.T) audit.Logger {
	t.Helper()
	chain, err := audit.NewFileLogger(filepath.Join(t.TempDir(), "audit-events.ndjson"))
	require.NoError(t, err)
	require.NoError(t, chain.Close())
	return audit.NewDecisionLogger(chain)
}

// TestBuildAuthMiddleware_AuditFailurePolicyDefaultsToFailClosed is the
// default-is-the-security-property assertion at the WIRING layer: an
// AuthMiddlewareConfig that names an AuditLogger and says nothing at all
// about the posture refuses a decision it could not record.
//
// The zero value of the config field carries this, which is exactly why it
// gets its own test rather than being inherited from a struct literal nobody
// checks.
func TestBuildAuthMiddleware_AuditFailurePolicyDefaultsToFailClosed(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
		AuditLogger: brokenSink(t),
		// AuditFailurePolicy deliberately unset.
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	err = mw.AuthorizeTenant(ctx, tenant.SystemTenantID, permission.AdminStatus)
	require.Error(t, err, "an unrecorded grant must be refused by default")

	var auditErr *middleware.AuditError
	assert.True(t, errors.As(err, &auditErr), "want *middleware.AuditError, got %T: %v", err, err)
	var writeErr *audit.WriteError
	assert.True(t, errors.As(err, &writeErr), "want the sink's *audit.WriteError inside, got %v", err)
}

// TestBuildAuthMiddleware_AuditFailurePolicyFailOpenIsHonoured proves the
// operator's deliberate escape reaches the middleware. A lever the config
// accepts and the wiring ignores is worse than no lever.
func TestBuildAuthMiddleware_AuditFailurePolicyFailOpenIsHonoured(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:            true,
		RoleService:        adminRoleService(),
		UserService:        &fakeUserService{},
		AuditLogger:        brokenSink(t),
		AuditFailurePolicy: middleware.AuditFailOpen,
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	assert.NoError(t, mw.AuthorizeTenant(ctx, tenant.SystemTenantID, permission.AdminStatus),
		"fail-open must permit the decision the sink could not record")
}

// TestBuildAuthMiddleware_HealthyAuditSinkAuthorizes is the control: the same
// wiring with a working sink authorizes normally. Without it, a middleware
// that refused everything would pass the default test above.
func TestBuildAuthMiddleware_HealthyAuditSinkAuthorizes(t *testing.T) {
	ring := audit.NewRingBuffer(8)
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
		AuditLogger: audit.NewDecisionLogger(ring),
	})
	require.NoError(t, err)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	require.NoError(t, mw.AuthorizeTenant(ctx, tenant.SystemTenantID, permission.AdminStatus))
	assert.Len(t, ring.Snapshot(), 1, "the decision must be in the trail")
}
