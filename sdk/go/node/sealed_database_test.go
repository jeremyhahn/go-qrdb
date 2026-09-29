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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A node composed without a database service registry has no way to reach a
// named database, so a client scoped to one is refused on its first call
// rather than served the default database -- and so a sealed database is
// never reachable through it either.
func TestEmbedded_DatabaseScopedWithoutRegistryRefuses(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	scoped, err := n.Client().DatabaseScoped("default")
	require.NoError(t, err, "scoping resolves per call; the refusal comes at the call")

	err = scoped.KV().Put(ctx, "k", []byte("v"))
	var unsupported *transport.CapabilityUnsupportedError
	require.Truef(t, errors.As(err, &unsupported), "Put without a registry = %v, want *transport.CapabilityUnsupportedError", err)
	_, err = scoped.KV().Get(ctx, "k")
	require.Truef(t, errors.As(err, &unsupported), "Get without a registry = %v, want *transport.CapabilityUnsupportedError", err)
}
