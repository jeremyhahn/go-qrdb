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

//go:build !integration

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// abacTestInner is a minimal inner authorizer for decorateWithABAC tests.
type abacTestInner struct {
	err   error
	calls int
}

func (a *abacTestInner) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	a.calls++
	return a.err
}

// TestDecorateWithABAC_Disabled verifies that a disabled ABAC config
// returns the inner authorizer unchanged (zero overhead).
func TestDecorateWithABAC_Disabled(t *testing.T) {
	inner := &abacTestInner{}
	got, err := decorateWithABAC(inner, qrdbconfig.AuthABACConfig{})
	require.NoError(t, err)
	assert.Same(t, auth.Authorizer(inner), got)
}

// TestDecorateWithABAC_MissingDir verifies the fail-closed posture: an
// enabled ABAC config with an unreadable policy dir aborts construction.
func TestDecorateWithABAC_MissingDir(t *testing.T) {
	inner := &abacTestInner{}
	_, err := decorateWithABAC(inner, qrdbconfig.AuthABACConfig{
		Enabled:   true,
		PolicyDir: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	require.Error(t, err)
	var dirErr *abac.PolicyDirError
	assert.True(t, errors.As(err, &dirErr))
}

// TestDecorateWithABAC_EnabledWithPolicies verifies the composed provider:
// an explicit deny from a dir-loaded policy overrides the inner allow,
// while unmatched requests delegate to the inner authorizer.
func TestDecorateWithABAC_EnabledWithPolicies(t *testing.T) {
	dir := t.TempDir()
	policy := `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: qrdbd-guardrail
  tenant: system
spec:
  subjects:
    any: true
  rules:
    - name: no-purge
      effect: deny
      actions: ["purge"]
      resources: ["/kv/**"]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "guardrail.yaml"), []byte(policy), 0o600))

	inner := &abacTestInner{}
	authz, err := decorateWithABAC(inner, qrdbconfig.AuthABACConfig{Enabled: true, PolicyDir: dir})
	require.NoError(t, err)

	identity := &auth.Identity{Subject: "admin", TenantID: tenant.SystemTenantID, Roles: []string{"admin"}}

	// Explicit deny overrides the inner allow; inner is never consulted.
	err = authz.Authorize(context.Background(), identity, "/kv/orders", "purge")
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrUnauthorized)
	var denied *abac.PolicyDeniedError
	require.True(t, errors.As(err, &denied))
	assert.Equal(t, "qrdbd-guardrail", denied.Policy)
	assert.Zero(t, inner.calls)

	// Unmatched requests delegate to the inner authorizer.
	require.NoError(t, authz.Authorize(context.Background(), identity, "/kv/orders", "read"))
	assert.Equal(t, 1, inner.calls)
}
