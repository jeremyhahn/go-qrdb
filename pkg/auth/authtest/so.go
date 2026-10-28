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

package authtest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// SOIdentity returns an identity holding the built-in SO role of its own
// tenant, by the identifier BuiltInRoles persists it under, and nothing else.
func SOIdentity(tenantID, subject string) *auth.Identity {
	return &auth.Identity{
		Subject:  subject,
		TenantID: tenantID,
		Roles:    []string{role.SystemRoleID(tenantID, role.RoleNameSO)},
	}
}

// GrantedNonSOIdentity returns an identity that RBAC admits to perms without
// the SO role: it holds no role, and the permissions arrive as
// ScopePermissions, the way a platform token issued for them does. It is the
// caller the SO gate exists to refuse even though authorization lets it
// through.
func GrantedNonSOIdentity(tenantID, subject string, perms ...permission.Permission) *auth.Identity {
	scope := make([]string, 0, len(perms))
	for _, p := range perms {
		scope = append(scope, string(p))
	}
	return &auth.Identity{
		Subject:          subject,
		TenantID:         tenantID,
		ScopePermissions: scope,
	}
}

// SOVerifier returns the real auth.SOAuthenticator over an in-memory store,
// with pin committed for every tenant in tenantIDs and an in-memory audit
// sink.
func SOVerifier(t testing.TB, pin string, tenantIDs ...string) *auth.SOAuthenticator {
	t.Helper()
	v, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("authtest: SO authenticator: %v", err)
	}
	for _, tenantID := range tenantIDs {
		if err := v.SetSOPIN(context.Background(), tenantID, []byte(pin)); err != nil {
			t.Fatalf("authtest: set SO PIN for %q: %v", tenantID, err)
		}
	}
	return v
}

// Barrier is a service.BarrierServicer that implements BarrierSeal,
// BarrierRekeyStart, BarrierOfficerRevoke, BarrierDeleteRecoveryKeys,
// BarrierGenerateRecoveryKeys, BarrierInitializeShamir, BarrierInitialize,
// BarrierUnseal and the node share-holder registry methods
// (BarrierOfficerRegister in either domain, BarrierOfficerList), and counts
// the calls that reached it, so a test can tell a refused request from one
// the service performed. Every other method is the nil embedded interface: a
// test that reaches one has left the path it meant to drive.
type Barrier struct {
	service.BarrierServicer
	// PINs is the barrier PIN backend the rekey and revocation SO PIN is
	// checked against, as the real barrier service checks it: refused with
	// no backend, refused while no SO PIN is provisioned, refused when wrong.
	// The fixtures set it to the backend the server under test is given.
	PINs             pin.PINBackend
	rekeys           atomic.Int64
	revocations      atomic.Int64
	deletes          atomic.Int64
	seals            atomic.Int64
	rewraps          atomic.Int64
	generations      atomic.Int64
	shamirInits      atomic.Int64
	officerRegs      atomic.Int64
	recoveryRegs     atomic.Int64
	officerReads     atomic.Int64
	officerUsernames sync.Map
	transitions      atomic.Int64
}

var _ service.BarrierServicer = (*Barrier)(nil)

// BarrierInitialize records the initialization.
func (b *Barrier) BarrierInitialize(context.Context, []byte) error {
	b.transitions.Add(1)
	return nil
}

// BarrierInitialized reports false, consistent with this double's
// BarrierInitialize, which records every call and never refuses one as
// already initialized.
func (b *Barrier) BarrierInitialized(context.Context) (bool, error) { return false, nil }

// BarrierUnseal records the unseal.
func (b *Barrier) BarrierUnseal(context.Context, []byte) error {
	b.transitions.Add(1)
	return nil
}

// Transitions reports how many initialize and unseal calls reached the
// service.
func (b *Barrier) Transitions() int64 { return b.transitions.Load() }

// BarrierOfficerRegister records the registration in domain and answers the
// way the real service does: an issued certificate.
func (b *Barrier) BarrierOfficerRegister(_ context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if domain == seal.DomainRecovery {
		b.recoveryRegs.Add(1)
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("certificate")}, nil
	}
	n := b.officerRegs.Add(1)
	b.officerUsernames.Store(req.Username, int(n))
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("certificate")}, nil
}

// OfficerRegistrations reports how many unseal-domain registrations reached
// the service.
func (b *Barrier) OfficerRegistrations() int64 { return b.officerRegs.Load() }

// RecoveryOfficerRegistrations reports how many recovery-domain
// registrations reached the service.
func (b *Barrier) RecoveryOfficerRegistrations() int64 { return b.recoveryRegs.Load() }

// BarrierOfficerList records the read and returns every unseal-domain holder
// registered through this double.
func (b *Barrier) BarrierOfficerList(context.Context, seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	b.officerReads.Add(1)
	var out []*service.BarrierOfficerStatus
	b.officerUsernames.Range(func(k, v any) bool {
		username, _ := k.(string)
		index, _ := v.(int)
		out = append(out, &service.BarrierOfficerStatus{Username: username, ShareIndex: index})
		return true
	})
	return out, nil
}

// OfficerReads reports how many holder list reads reached the service.
func (b *Barrier) OfficerReads() int64 { return b.officerReads.Load() }

// BarrierGenerateRecoveryKeys records the generation.
func (b *Barrier) BarrierGenerateRecoveryKeys(_ context.Context, threshold int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	b.generations.Add(1)
	return &service.RecoveryKeysInfo{Threshold: threshold, Total: threshold, ShareCount: threshold}, nil
}

// RecoveryGenerations reports how many recovery-key generations reached the
// service.
func (b *Barrier) RecoveryGenerations() int64 { return b.generations.Load() }

// BarrierInitializeShamir records the initialization.
func (b *Barrier) BarrierInitializeShamir(_ context.Context, threshold, totalShares int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	b.shamirInits.Add(1)
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: totalShares, ShareCount: totalShares}, nil
}

// ShamirInits reports how many Shamir initializations reached the service.
func (b *Barrier) ShamirInits() int64 { return b.shamirInits.Load() }

// BarrierSeal records the seal.
func (b *Barrier) BarrierSeal(context.Context) error {
	b.seals.Add(1)
	return nil
}

// Seals reports how many seals reached the service.
func (b *Barrier) Seals() int64 { return b.seals.Load() }

// BarrierChangePassphrase records the passphrase change.
func (b *Barrier) BarrierChangePassphrase(context.Context, []byte, []byte) error {
	b.rewraps.Add(1)
	return nil
}

// PassphraseChanges returns how many passphrase changes reached the service.
func (b *Barrier) PassphraseChanges() int64 { return b.rewraps.Load() }

// BarrierDeleteRecoveryKeys records the deletion.
func (b *Barrier) BarrierDeleteRecoveryKeys(context.Context) error {
	b.deletes.Add(1)
	return nil
}

// RecoveryDeletes reports how many recovery-key deletions reached the service.
func (b *Barrier) RecoveryDeletes() int64 { return b.deletes.Load() }

// BarrierPINBackend returns the real software pin.PINBackend over an
// in-memory store, with soPIN committed as the barrier SO PIN, so a transport
// that verifies a request's so_pin against it is tested against the real
// verifier.
func BarrierPINBackend(t testing.TB, soPIN string) pin.PINBackend {
	t.Helper()
	b := EmptyBarrierPINBackend(t)
	if err := b.SetSOPIN("", soPIN); err != nil {
		t.Fatalf("authtest: set barrier SO PIN: %v", err)
	}
	return b
}

// EmptyBarrierPINBackend returns the real software pin.PINBackend over an
// in-memory store with no barrier SO PIN committed yet, so a test can observe
// the PIN an initializing request sets.
func EmptyBarrierPINBackend(t testing.TB) pin.PINBackend {
	t.Helper()
	b, err := pin.NewSoftwareBackend(&memPINStore{kv: map[string][]byte{}}, pin.HashConfig{
		Algorithm: pin.HashArgon2id, Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16,
	})
	if err != nil {
		t.Fatalf("authtest: PIN backend: %v", err)
	}
	return b
}

// Ceremony is a service.CeremonyServicer that implements CeremonyInitialize
// and CeremonySignCSR and records the SO PIN each request handed the service,
// so a test can tell which PIN the request's so_pin field carried. Every
// other method is the nil embedded interface.
type Ceremony struct {
	service.CeremonyServicer
	mu     sync.Mutex
	soPINs []string
}

// CeremonyInitialize records the request's SO PIN.
func (c *Ceremony) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	var soPIN string
	if opts != nil {
		soPIN = opts.SOPin
	}
	c.record(soPIN)
	return &service.CeremonyInitResult{State: "initialized"}, nil
}

// CeremonySignCSR records the request's SO PIN.
func (c *Ceremony) CeremonySignCSR(_ context.Context, _, soPIN, _, _ string) (*service.CeremonySignCSRResult, error) {
	c.record(soPIN)
	return &service.CeremonySignCSRResult{CertPEM: []byte("certificate")}, nil
}

func (c *Ceremony) record(soPIN string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.soPINs = append(c.soPINs, soPIN)
}

// SOPINs returns the SO PINs that reached the service, in order.
func (c *Ceremony) SOPINs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.soPINs...)
}

// CeremonyCA is a ceremony.CeremonyCA a transport accepts as configured. The
// Ceremony double never calls it; every method is the nil embedded interface.
type CeremonyCA struct {
	ceremony.CeremonyCA
}

// memPINStore is the in-memory pin.Storage BarrierPINBackend runs over.
type memPINStore struct {
	mu sync.Mutex
	kv map[string][]byte
}

func (m *memPINStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[key]
	if !ok {
		return nil, pin.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (m *memPINStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[key] = append([]byte(nil), value...)
	return nil
}

// Delete implements pin.Storage. Deleting an absent key is a no-op.
func (m *memPINStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.kv, key)
	return nil
}

// BarrierRekeyStart checks the SO PIN against PINs and records the rekey
// round.
func (b *Barrier) BarrierRekeyStart(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, soPIN string) (*service.QuorumRoundInfo, error) {
	if err := pin.RequireProvisionedSOPIN(ctx, b.PINs, soPIN); err != nil {
		return nil, err
	}
	b.rekeys.Add(1)
	return &service.QuorumRoundInfo{Domain: domain, Op: seal.OpRekey, Required: threshold, NewThreshold: threshold, Policy: policy}, nil
}

// Rekeys reports how many rekey rounds reached the service.
func (b *Barrier) Rekeys() int64 { return b.rekeys.Load() }

// BarrierOfficerRevoke checks the SO PIN against PINs and records the
// revocation round.
func (b *Barrier) BarrierOfficerRevoke(ctx context.Context, domain seal.QuorumDomain, username string, threshold int, soPIN string) (*service.QuorumRoundInfo, error) {
	if err := pin.RequireProvisionedSOPIN(ctx, b.PINs, soPIN); err != nil {
		return nil, err
	}
	b.revocations.Add(1)
	return &service.QuorumRoundInfo{Domain: domain, Op: seal.OpRekey, Required: threshold, NewThreshold: threshold, Remove: []string{username}}, nil
}

// Revocations reports how many revocation rounds reached the service.
func (b *Barrier) Revocations() int64 { return b.revocations.Load() }
