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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestNode_EmbeddedClientRefusesWhileSealed pins the node's in-process client
// to the gate its network listeners enforce: once the node barrier is sealed
// through it, the next data request is refused with the sealed refusal, and
// barrier status -- on the sealed-window allowlist -- still answers.
func TestNode_EmbeddedClientRefusesWhileSealed(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("test-secret")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	ctx := context.Background()
	client := n.Client()
	require.NoError(t, client.KV().Put(ctx, "k", []byte("v")))
	require.NoError(t, client.Barrier().BarrierSeal(ctx))

	err = client.KV().Put(ctx, "k", []byte("v"))
	require.Error(t, err, "the in-process client wrote through a sealed node barrier")
	require.True(t, errors.Is(err, seal.ErrSealed), "err = %v, want the sealed refusal", err)

	st, err := client.Barrier().BarrierStatus(ctx)
	require.NoError(t, err, "barrier status is on the sealed-window allowlist")
	require.True(t, st.Sealed)
}
