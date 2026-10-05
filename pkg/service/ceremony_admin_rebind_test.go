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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// fakeAdminLookup is a minimal firstboot.AdminLookup AND firstboot.CertBinder
// test double: BindAdminCert appends to u.CertBindings exactly as a real
// store's AddCertBinding would, so a test can assert the reissue path
// actually bound the certificate it minted (go-qrdb#548 review: B5), not
// just that it returned one.
type fakeAdminLookup struct {
	u       *user.User
	err     error
	bindErr error
	binds   int
}

func (f *fakeAdminLookup) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.u, nil
}

func (f *fakeAdminLookup) BindAdminCert(_ context.Context, _ string, _ uint64, thumbprint string) error {
	f.binds++
	if f.bindErr != nil {
		return f.bindErr
	}
	if f.u != nil {
		f.u.CertBindings = append(f.u.CertBindings, user.CertBinding{
			Thumbprint: thumbprint, Kind: user.CertBindingSPKIThumbprint,
		})
	}
	return nil
}

// TestCeremonyServiceWithAdminRebind_DelegatesBeforeInitialize covers the
// pass-through path: before the ceremony is initialized, CeremonyInitialize
// behaves exactly as the wrapped servicer's. The binder is nil and must
// never be reached: this path never calls ReissueAdminCredential at all.
func TestCeremonyServiceWithAdminRebind_DelegatesBeforeInitialize(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, &fakeAdminLookup{err: &user.UserNotFoundError{}}, nil, nil)
	result, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.AdminCertPEM)
}

// TestCeremonyServiceWithAdminRebind_RefusesWhenAlreadyBound covers the
// refusal: a second CeremonyInitialize call after the admin already holds a
// live binding is refused, not treated as a retry. The binder is nil and
// must never be reached: the refusal happens before ReissueAdminCredential.
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
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, bound, nil, nil)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	_, err = wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", AdminGenerate: true,
	})
	var already *AdminAlreadyBoundError
	require.ErrorAs(t, err, &already)
}

// TestCeremonyServiceWithAdminRebind_ReissuesWhenUnbound covers the recovery
// path (go-qrdb#548): re-running CeremonyInitialize on an already-initialized
// ceremony, with no live admin binding, reissues a fresh credential rather
// than refusing with ceremony.ErrAlreadyInitialized. It must also BIND that
// fresh credential (go-qrdb#548 review: B5) -- a caller re-running `qrdb
// init` to repair a lost binding is asking for a working login, not a
// certificate to bind by hand.
func TestCeremonyServiceWithAdminRebind_ReissuesWhenUnbound(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
	}}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unbound, unbound, nil)

	first, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	second, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
	require.NoError(t, err)
	assert.NotEmpty(t, second.AdminCertPEM)
	assert.NotEqual(t, first.AdminCertPEM, second.AdminCertPEM,
		"a retry must mint a fresh credential, not replay the first")

	if unbound.binds != 1 {
		t.Fatalf("BindAdminCert calls = %d, want exactly 1 (the second, reissuing call)", unbound.binds)
	}
	if len(unbound.u.CertBindings) != 1 {
		t.Fatalf("admin CertBindings = %+v, want exactly one binding for the reissued certificate", unbound.u.CertBindings)
	}
}

// TestCeremonyServiceWithAdminRebind_PropagatesUnverifiableLookup covers the
// fail-closed branch: a lookup failure other than "no binding" is NOT treated
// as license to retry. The binder is nil and must never be reached.
func TestCeremonyServiceWithAdminRebind_PropagatesUnverifiableLookup(t *testing.T) {
	saved := AdminRebindLookupTimeout
	AdminRebindLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { AdminRebindLookupTimeout = saved })

	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unverifiable := &fakeAdminLookup{err: errors.New("store unavailable")}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unverifiable, nil, nil)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	_, err = wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", AdminGenerate: true,
	})
	require.Error(t, err)
	var already *AdminAlreadyBoundError
	assert.False(t, errors.As(err, &already), "an unverifiable lookup must not be treated as unbound")
	var lookupErr *firstboot.AdminLookupError
	assert.True(t, errors.As(err, &lookupErr), "err = %T(%v), want *firstboot.AdminLookupError", err, err)
}

// ---------------------------------------------------------------------------
// B5: reissue never returns an unbound admin credential (go-qrdb#548 review)
// ---------------------------------------------------------------------------

// TestCeremonyServiceWithAdminRebind_NoBinderRefuses proves a reissue with no
// CertBinder wired is refused rather than returned as a successful-looking
// credential nothing is wired to use.
func TestCeremonyServiceWithAdminRebind_NoBinderRefuses(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
	}}
	// users is wired (the lookup that gates the retry), binder is not.
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unbound, nil, nil)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	result, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
	require.Nil(t, result, "a reissue this wrapper cannot bind must never return a credential")
	var noBinder *AdminRebindNoBinderError
	require.ErrorAs(t, err, &noBinder)
}

// TestCeremonyServiceWithAdminRebind_BindFailureRefuses proves a reissue
// whose bind call itself fails is reported as an error, never silently
// returned as success with the certificate unbound.
func TestCeremonyServiceWithAdminRebind_BindFailureRefuses(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
	}}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unbound, unbound, nil)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	unbound.bindErr = errors.New("store unavailable")
	result, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
	require.Nil(t, result, "a failed bind must never return a credential")
	var bindErr *AdminRebindBindError
	require.ErrorAs(t, err, &bindErr)
	if len(unbound.u.CertBindings) != 0 {
		t.Fatalf("admin CertBindings = %+v, want none: the failed bind must not have partially applied", unbound.u.CertBindings)
	}
}

// fakeAdminBoundNotifier is a minimal firstboot.AdminBoundNotifier test
// double: it counts AdminBound calls so a test can assert the notifier is
// told exactly once per successful bind, and never on a refusal.
type fakeAdminBoundNotifier struct{ calls int }

func (f *fakeAdminBoundNotifier) AdminBound() { f.calls++ }

// TestCeremonyServiceWithAdminRebind_NotifiesOnSuccessfulBind covers
// go-qrdb#560 review B1's replacement for the awaiting-admin-credential
// gate's live per-request probe: a successful reissue-and-bind calls the
// wired AdminBoundNotifier exactly once, so pkg/firstboot's
// AwaitingAdminCredentialProbe can leave "awaiting" without re-reading the
// user store.
func TestCeremonyServiceWithAdminRebind_NotifiesOnSuccessfulBind(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
	}}
	notifier := &fakeAdminBoundNotifier{}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unbound, unbound, notifier)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)
	if notifier.calls != 0 {
		t.Fatalf("AdminBound calls after the FIRST CeremonyInitialize = %d, want 0: that call never reaches ReissueAdminCredential", notifier.calls)
	}

	_, err = wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
	require.NoError(t, err)
	if notifier.calls != 1 {
		t.Fatalf("AdminBound calls after the reissuing retry = %d, want exactly 1", notifier.calls)
	}
}

// TestCeremonyServiceWithAdminRebind_NoNotifierOnRefusal proves a refused
// reissue (no CertBinder wired, so the bind never happens) never calls
// AdminBound: the notifier must reflect only a REAL bind, never a refusal
// that merely resembled one.
func TestCeremonyServiceWithAdminRebind_NoNotifierOnRefusal(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	unbound := &fakeAdminLookup{u: &user.User{
		ID: 1, Username: "admin", TenantID: tenantid.System,
		Lifecycle: user.LifecycleActive,
	}}
	notifier := &fakeAdminBoundNotifier{}
	// users is wired (the lookup that gates the retry), binder is not.
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, unbound, nil, notifier)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	_, err = wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
	var noBinder *AdminRebindNoBinderError
	require.ErrorAs(t, err, &noBinder)
	if notifier.calls != 0 {
		t.Fatalf("AdminBound calls on a refused reissue = %d, want 0", notifier.calls)
	}
}

// TestCeremonyServiceWithAdminRebind_AdminNotFoundRefuses covers the window
// before first boot has created the admin user at all: a reissue in that
// window has nothing to bind the fresh certificate to, so it is refused --
// the reissued certificate is discarded, not handed back unbound.
func TestCeremonyServiceWithAdminRebind_AdminNotFoundRefuses(t *testing.T) {
	inner, ca := newTestCeremonyService(t)
	core, ok := CeremonyServiceCore(inner)
	require.True(t, ok)

	// u == nil and err == nil: GetUserByUsername answers (nil, nil), which
	// VerifyAdminCertBinding's own predicate reads as AdminBindingMissing
	// (first boot has not created the admin yet) -- one of the two reasons
	// a retry may proceed on, per CeremonyInitialize's own switch.
	missing := &fakeAdminLookup{}
	wrapped := NewCeremonyServiceWithAdminRebind(inner, core, missing, missing, nil)

	_, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	result, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
	require.Nil(t, result, "a reissue with no admin to bind to must never return a credential")
	var notFound *AdminRebindAdminNotFoundError
	require.ErrorAs(t, err, &notFound)
	if missing.binds != 0 {
		t.Fatalf("BindAdminCert calls = %d, want 0: there is no admin ID to bind to", missing.binds)
	}
}

// TestCeremonyServiceWithAdminRebind_RestartThenAwaitingRetry is the
// go-qrdb#560 review fix: "after a restart the ceremony state is lost" --
// a fresh *ceremony.CeremonyService over the SAME barrier and PIN backend
// (a restart) must NOT treat CeremonyInitialize as a first-ever Initialize
// (which could mint an unbound certificate and re-provision the SO/User
// PINs for whoever calls first). It drives the exact sequence the
// awaiting-admin-credential retry takes after a restart: a wrong SO PIN is
// refused, and the CORRECT SO PIN binds and notifies on the FIRST attempt
// -- not a second one, which would mean the first attempt was consumed by
// a spurious Initialize replay rather than refused outright.
func TestCeremonyServiceWithAdminRebind_RestartThenAwaitingRetry(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	pins := pintest.NewBackend(t)
	ca := newTestCeremonyCA(t)

	core1, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	core1.SetPINBackend(pins)
	_, err = NewCeremonyService(core1).CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)
	require.Equal(t, ceremony.StateOperational, core1.State())

	// "restart": a FRESH core over the SAME barrier + PIN store -- no Go
	// value carries over, only what the barrier and PIN backend persisted.
	core2, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	core2.SetPINBackend(pins)
	core2.SetCA(ca) // the composer's boot sequence re-wires the CA on every boot
	require.Equal(t, ceremony.StateOperational, core2.State(),
		"state must be restored across the restart, not reset to awaiting_init")

	unbound := &fakeAdminLookup{u: &user.User{ID: 1, Username: "admin", TenantID: tenantid.System, Lifecycle: user.LifecycleActive}}
	notifier := &fakeAdminBoundNotifier{}
	wrapped := NewCeremonyServiceWithAdminRebind(NewCeremonyService(core2), core2, unbound, unbound, notifier)

	_, werr := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "wrong", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.Error(t, werr, "a wrong SO PIN after a restart must be refused, not treated as a fresh Initialize")
	assert.False(t, errors.Is(werr, ceremony.ErrAlreadyInitialized), "the state must be read as already-initialized, not re-run through Initialize at all")
	assert.Equal(t, 0, unbound.binds, "a refused reissue must never bind anything")

	res, err := wrapped.CeremonyInitialize(context.Background(), ca, &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err, "the correct SO PIN must bind on the FIRST attempt after a restart")
	require.NotNil(t, res)
	assert.NotEmpty(t, res.AdminCertPEM)
	assert.Equal(t, 1, unbound.binds)
	assert.Equal(t, 1, notifier.calls)
}

// restoreFailBackend fails every Get under rootKeyPath except the bare root
// key path itself, so a barrier built over it answers
// CeremonyOperational/Initialized with a real error -- the restore-state
// read failure this package's own re-review fix (StateRestoreFailed) is for.
type restoreFailBackend struct {
	seal.StorageBackend
	rootKeyPath string
}

func (b *restoreFailBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if key != b.rootKeyPath && strings.HasPrefix(key, b.rootKeyPath) {
		return nil, errors.New("service test: injected restore-state read failure")
	}
	return b.StorageBackend.Get(ctx, key)
}

// TestCeremonyServiceWithAdminRebind_RefusesWhenRestoreFailed proves the
// wrapper's awaiting-passthrough does not mistake StateRestoreFailed for
// "not yet initialized": before this fix, only StateAwaitingInit short-
// circuited to the wrapped Initialize, so StateRestoreFailed fell through
// to the live-binding check below it -- a check that assumes the ceremony
// IS already initialized, which this process cannot actually confirm.
func TestCeremonyServiceWithAdminRebind_RefusesWhenRestoreFailed(t *testing.T) {
	const rootKeyPath = "root-key"
	backend := &restoreFailBackend{StorageBackend: seal.NewMemoryBackend(), rootKeyPath: rootKeyPath}
	sw, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-key-32bytes!abcdefgh"))
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(testLogger(), backend, seal.BarrierConfig{
		RootKeyPath:     rootKeyPath,
		PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase},
	}, sw)
	require.NoError(t, err)
	t.Cleanup(func() { _ = barrier.Close() })

	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	core.SetPINBackend(pintest.NewBackend(t))
	require.Equal(t, ceremony.StateRestoreFailed, core.State())

	wrapped := NewCeremonyServiceWithAdminRebind(NewCeremonyService(core), core, &fakeAdminLookup{err: &user.UserNotFoundError{}}, nil, nil)
	_, werr := wrapped.CeremonyInitialize(context.Background(), newTestCeremonyCA(t), &CeremonyInitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.ErrorIs(t, werr, ceremony.ErrRestoreFailed)
}
