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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// secondReadHook is the system holder authority as the barrier's
// distributor, running hook just before the barrier's second read of the
// holder set: the read that splits the key, once the barrier already reads as
// initialized.
type secondReadHook struct {
	seal.CeremonyDistributor
	calls int
	hook  func()
}

func (d *secondReadHook) PrepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	d.calls++
	if d.calls == 2 && d.hook != nil {
		d.hook()
	}
	return d.CeremonyDistributor.PrepareRoster(ctx, domain, threshold, policy, exclude)
}

// sealHook is the holder authority as the barrier's distributor, running
// hook just before it seals the shares (Distribute): after the roster was
// read, before the holders' records are read again.
type sealHook struct {
	seal.CeremonyDistributor
	calls int
	hook  func()
}

func (d *sealHook) Distribute(ctx context.Context, secret *secure.Buffer, roster *seal.HolderRoster, digest []byte) (int, error) {
	d.calls++
	if d.calls == 1 && d.hook != nil {
		d.hook()
	}
	return d.CeremonyDistributor.Distribute(ctx, secret, roster, digest)
}

// TestBarrierService_GenerateRecoveryKeysRefusesARacingRegistration pins
// that the barrier service generates the recovery key through the recovery
// holder authority: a recovery holder registered between the roster read and
// the sealing of the shares is refused with
// ceremony.ErrInitializationInProgress, and the generation completes with
// the holders its roster named.
func TestBarrierService_GenerateRecoveryKeysRefusesARacingRegistration(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"))
	_, err := e.svc.BarrierInitializeShamir(adminCtx(), 2, 2, "")
	require.NoError(t, err)
	e.register(seal.DomainRecovery, newSvcHolder(t, "rita"), newSvcHolder(t, "rob"))
	dist := &sealHook{CeremonyDistributor: e.core}
	e.barrier.SetCeremonyDistributor(dist)
	var registerErr error
	dist.hook = func() {
		ruth := newSvcHolder(t, "ruth")
		_, registerErr = e.svc.BarrierOfficerRegister(adminCtx(), seal.DomainRecovery, &BarrierOfficerRegisterRequest{Username: ruth.name, CSRPEM: ruth.csr})
	}

	info, err := e.svc.BarrierGenerateRecoveryKeys(adminCtx(), 2, "")
	require.NoError(t, err, "the generation completes")
	assert.Equal(t, 2, info.ShareCount)
	require.Equal(t, 1, dist.calls, "the hook ran before the shares were sealed")
	require.ErrorIs(t, registerErr, ceremony.ErrInitializationInProgress)
	list, err := e.svc.BarrierOfficerList(context.Background(), seal.DomainRecovery)
	require.NoError(t, err)
	assert.Len(t, list, 2, "ruth was not enrolled")
}

// TestBarrierService_InitializeShamirRefusesARacingRegistration pins that
// the barrier service initializes the system barrier through its holder
// authority: an unseal-key holder registered just before the barrier's second
// read is refused with ceremony.ErrInitializationInProgress, and the
// initialization completes, rather than failing the split with more holders
// than shares and leaving the barrier to need a restart.
func TestBarrierService_InitializeShamirRefusesARacingRegistration(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"))
	dist := &secondReadHook{CeremonyDistributor: e.core}
	e.barrier.SetCeremonyDistributor(dist)
	var registerErr error
	dist.hook = func() {
		carol := newSvcHolder(t, "carol")
		_, registerErr = e.svc.BarrierOfficerRegister(adminCtx(), seal.DomainUnseal, &BarrierOfficerRegisterRequest{Username: carol.name, CSRPEM: carol.csr})
	}

	info, err := e.svc.BarrierInitializeShamir(adminCtx(), 2, 2, "")
	require.NoError(t, err, "the initialization completes")
	assert.Equal(t, 2, info.ShareCount)
	require.Equal(t, 2, dist.calls, "the barrier read the holder set twice, the hook before the second")
	require.ErrorIs(t, registerErr, ceremony.ErrInitializationInProgress)
	list, err := e.svc.BarrierOfficerList(context.Background(), seal.DomainUnseal)
	require.NoError(t, err)
	assert.Len(t, list, 2, "carol was not enrolled")
}
