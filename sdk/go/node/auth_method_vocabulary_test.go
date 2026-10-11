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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// oauthAuthenticator stands in for an embedder's bearer-token validator: it
// authenticates nothing here, and only its Name matters.
type oauthAuthenticator struct{}

func (oauthAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return nil, auth.ErrUnauthenticated
}

func (oauthAuthenticator) Name() string { return "oauth" }

// TestAuthMethodVocabulary_IsPerNode: the permitted-method vocabulary is the
// built-in set plus THIS node's authenticator chain. Two nodes in one process
// must not share it — a method one node's chain registers must not become
// writable on a node that cannot authenticate with it.
func TestAuthMethodVocabulary_IsPerNode(t *testing.T) {
	ctx := context.Background()
	policy := []string{auth.MethodMTLS, "oauth"}

	withOAuth, err := NewStandaloneNode(ctx, newMemoryNodeCfg(t), nil, WithAuthenticators(auth.ChainAfterMTLS, oauthAuthenticator{}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = withOAuth.Close() })

	without, err := NewStandaloneNode(ctx, newMemoryNodeCfg(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = without.Close() })

	ctx = tenant.WithTenantID(ctx, "t1")
	u, err := withOAuth.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "olga", Email: "olga@example.com", TenantID: "t1", AuthMethods: policy,
	})
	require.NoError(t, err, "the node whose chain has oauth must accept it")
	require.Equal(t, policy, u.AuthMethods)

	_, err = without.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "olga", Email: "olga@example.com", TenantID: "t1", AuthMethods: policy,
	})
	var unknown *user.UnknownAuthMethodError
	require.True(t, errors.As(err, &unknown), "the node without oauth in its chain accepted it: err = %v", err)
	require.Equal(t, "oauth", unknown.Method)
}
