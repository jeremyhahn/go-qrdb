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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// TestBootstrapAuthEnforcement_InvalidRevocationTTLFailsBoot pins the
// fail-loud posture on a security window: an unparseable
// auth.revocation_cache_ttl aborts the boot rather than quietly reverting to
// the default. A node running with a window the operator did not choose is
// worse than a node that refused to start.
func TestBootstrapAuthEnforcement_InvalidRevocationTTLFailsBoot(t *testing.T) {
	store := make(map[string][]byte)
	mock := newMockKVStore(store)
	tx := newMockTransactionServicer(store)
	node := &mockRaftNode{}
	mgmt, err := bootstrapUserRoleStores(
		context.Background(), mock, mock, tx, node, testLeaderWait,
		1, 1, &mockTenantServicer{}, false, nil, "", nil,
		auth.MTLSBindingEnrolled, nil, nil, nil, soTrustSeams{},
	)
	require.NoError(t, err)

	_, err = bootstrapAuthEnforcement(
		context.Background(), mgmt, t.TempDir(), node,
		audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{},
		nil, nil, nil, nil, nil, "not-a-duration", "", "",
		firstboot.NewAwaitingAdminCredentialProbe(), nil,
	)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "auth.revocation_cache_ttl", abe.Step)
}

// TestApplyConfigToFlags_RevocationCacheTTL verifies the configured window
// actually reaches the flag the auth bootstrap reads. A value that stops at
// the config struct is a setting the operator can write and the node will
// ignore.
func TestApplyConfigToFlags_RevocationCacheTTL(t *testing.T) {
	cmd := &cobra.Command{}
	f := &startFlags{}
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.RevocationCacheTTL = "5s"

	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "5s", f.revocationCacheTTL)
}
