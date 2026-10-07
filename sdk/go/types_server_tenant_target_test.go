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

package sdk_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// fakeTenantTargetAuthorizer is a canned sdk.TenantTargetAuthorizer, so a
// module written against the SDK alone can be exercised without a real
// *AuthMiddleware.
type fakeTenantTargetAuthorizer struct {
	gotTenantID string
	gotPerm     permission.Permission
	err         error
}

func (f *fakeTenantTargetAuthorizer) AuthorizeTenantTarget(_ context.Context, tenantID string, perm permission.Permission) error {
	f.gotTenantID = tenantID
	f.gotPerm = perm
	return f.err
}

// targetAwareModule is the module shape a composing host writes against the
// SDK alone: it stashes the authorizer the server hands it and uses it from
// a handler on whichever transport it registers.
type targetAwareModule struct {
	sdk.BaseModule
	authz sdk.TenantTargetAuthorizer
}

func (m *targetAwareModule) Name() string { return "target-aware" }

func (m *targetAwareModule) SetTenantTargetAuthorizer(a sdk.TenantTargetAuthorizer) {
	m.authz = a
}

// Compile-time: a module written against the SDK names is the server's own
// extension -- the aliases are the pkg/apimodule declarations, not copies.
var (
	_ sdk.ServiceModule                 = (*targetAwareModule)(nil)
	_ sdk.ModuleTenantTargetAware       = (*targetAwareModule)(nil)
	_ apimodule.ModuleTenantTargetAware = (*targetAwareModule)(nil)
	_ apimodule.TenantTargetAuthorizer  = (*fakeTenantTargetAuthorizer)(nil)
	_ sdk.TenantTargetAuthorizer        = (*fakeTenantTargetAuthorizer)(nil)
)

// TestSDK_ModuleTenantTargetAware_ReachesTheAuthorizer verifies an
// SDK-written module receives the authorizer via SetTenantTargetAuthorizer
// and can call AuthorizeTenantTarget through it, entirely through SDK
// names (go-qrdb#587).
func TestSDK_ModuleTenantTargetAware_ReachesTheAuthorizer(t *testing.T) {
	m := &targetAwareModule{}
	wantErr := errors.New("denied")
	fake := &fakeTenantTargetAuthorizer{err: wantErr}

	m.SetTenantTargetAuthorizer(fake)
	if m.authz == nil {
		t.Fatal("SetTenantTargetAuthorizer did not record the authorizer")
	}

	err := m.authz.AuthorizeTenantTarget(context.Background(), "acme", permission.DatabasesRead)
	if !errors.Is(err, wantErr) {
		t.Errorf("AuthorizeTenantTarget err = %v, want the fake's canned denial", err)
	}
	if fake.gotTenantID != "acme" || fake.gotPerm != permission.DatabasesRead {
		t.Errorf("fake saw (%q, %s), want (%q, %s)", fake.gotTenantID, fake.gotPerm, "acme", permission.DatabasesRead)
	}
}
