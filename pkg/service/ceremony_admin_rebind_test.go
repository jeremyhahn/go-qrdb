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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// fakeAdminLookup is a minimal firstboot.AdminLookup test double.
type fakeAdminLookup struct {
	u   *user.User
	err error
}

func (f *fakeAdminLookup) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.u, nil
}

// TestCeremonyServiceWithAdminRebind_DelegatesBeforeInitialize covers the
// pass-through path: before the ceremony is initialized, CeremonyInitialize
// behaves exactly as the wrapped servicer's.
func TestCeremonyServiceWithAdminRebind_DelegatesBeforeInitialize(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, &fakeAdminLookup{err: &user.UserNotFoundError{}})
	result, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.AdminCertPEM)
}

// TestCeremonyServiceWithAdminRebind_RefusesWhenAlreadyBound covers the
// refusal: a second CeremonyInitialize call after the admin already holds a
// live binding is refused, not treated as a retry.
func TestCeremonyServiceWithAdminRebind_RefusesWhenAlreadyBound(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	bound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
		CertBindings: []user.CertBinding{{
			Thumbprint: "thumb-1", Kind: user.CertBindingSPKIThumbprint,
		}},
	}}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, bound)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	_, err = wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{AdminGenerate: true})
	var already *AdminAlreadyBoundError
	require.ErrorAs(t, err, &already)
}

// TestCeremonyServiceWithAdminRebind_ReissuesWhenUnbound covers the recovery
// path (go-qrdb#548): re-running CeremonyInitialize on an already-initialized
// ceremony, with no live admin binding, reissues a fresh credential rather
// than refusing with ceremony.ErrAlreadyInitialized.
func TestCeremonyServiceWithAdminRebind_ReissuesWhenUnbound(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
	}}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unbound)

	first, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	second, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{AdminGenerate: true})
	require.NoError(t, err)
	assert.NotEmpty(t, second.AdminCertPEM)
	assert.NotEqual(t, first.AdminCertPEM, second.AdminCertPEM,
		"a retry must mint a fresh credential, not replay the first")
}

// TestCeremonyServiceWithAdminRebind_PropagatesUnverifiableLookup covers the
// fail-closed branch: a lookup failure other than "no binding" is NOT treated
// as license to retry.
func TestCeremonyServiceWithAdminRebind_PropagatesUnverifiableLookup(t *testing.T) {
	saved := AdminRebindLookupTimeout
	AdminRebindLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { AdminRebindLookupTimeout = saved })

	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unverifiable := &fakeAdminLookup{err: errors.New("store unavailable")}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unverifiable)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	_, err = wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{AdminGenerate: true})
	require.Error(t, err)
	var already *AdminAlreadyBoundError
	assert.False(t, errors.As(err, &already), "an unverifiable lookup must not be treated as unbound")
	var lookupErr *firstboot.AdminLookupError
	assert.True(t, errors.As(err, &lookupErr), "err = %T(%v), want *firstboot.AdminLookupError", err, err)
}
