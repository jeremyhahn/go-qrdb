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

package wiring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// The attested-caller gate as it is WIRED (go-qrdb#278): the middleware tests
// prove what the gate does; this proves BuildAuthMiddleware -- the one
// constructor qrdbd and the SDK node share -- installs it.

// refusingVerifier cannot decide, which the gate must treat as a refusal.
type refusingVerifier struct{ calls int }

func (v *refusingVerifier) VerifyPeer(context.Context, auth.AttestedPeer) (*auth.PeerAttestation, error) {
	v.calls++
	return nil, errors.New("evidence cache unavailable")
}

func TestBuildAuthMiddleware_PeerAttestationGateInstalled(t *testing.T) {
	v := &refusingVerifier{}
	gate, err := middleware.NewPeerAttestationGate(v, middleware.PeerAttestationPolicy{
		Permissions:    []permission.Permission{permission.AdminStatus},
		MaxEvidenceAge: time.Minute,
	})
	require.NoError(t, err)

	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:         true,
		RoleService:     adminRoleService(),
		UserService:     &fakeUserService{},
		PeerAttestation: gate,
	})
	require.NoError(t, err)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	err = mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus)
	var refusal *middleware.PeerAttestationRefusedError
	require.True(t, errors.As(err, &refusal), "err = %v, want the gate's refusal", err)
	// No certificate on this context: refused before the verifier is asked.
	assert.Equal(t, middleware.PeerAttestationNoPeerCertificate, refusal.Reason)
}

func TestBuildAuthMiddleware_NoPeerAttestationGateGatesNothing(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
	})
	require.NoError(t, err)
	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	assert.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))
}
