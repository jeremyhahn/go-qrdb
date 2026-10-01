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

package node

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// passingVerifier is never consulted by these tests; the gate only has to be
// constructible.
type passingVerifier struct{}

func (passingVerifier) VerifyPeer(_ context.Context, p auth.AttestedPeer) (*auth.PeerAttestation, error) {
	return &auth.PeerAttestation{PeerThumbprint: p.Thumbprint, CollectedAt: time.Now(), Passed: true}, nil
}

// The option carries the gate from the node constructor toward
// wiring.AuthMiddlewareConfig.PeerAttestation (go-qrdb#278); that the wiring
// installs it is pinned in pkg/auth/wiring.
func TestWithPeerAttestationGate_SetsOption(t *testing.T) {
	gate, err := middleware.NewPeerAttestationGate(passingVerifier{}, middleware.PeerAttestationPolicy{
		Permissions:    []permission.Permission{permission.KVGet},
		MaxEvidenceAge: time.Minute,
	})
	require.NoError(t, err)
	o := &nodeOptions{}
	WithPeerAttestationGate(gate)(o)
	assert.Same(t, gate, o.peerAttestation)
}

func TestWithPeerAttestationGate_NilGatesNothing(t *testing.T) {
	o := &nodeOptions{}
	WithPeerAttestationGate(nil)(o)
	assert.Nil(t, o.peerAttestation)
}
