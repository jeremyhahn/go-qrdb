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

package seal

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// HolderCountMismatchError is returned when a distribution is asked for a
// specific number of shares but a different number of holders is enrolled.
type HolderCountMismatchError struct {
	// Want is the requested share count.
	Want int
	// Enrolled is the number of active holders.
	Enrolled int
}

// Error returns a human-readable description.
func (e *HolderCountMismatchError) Error() string {
	return "qrdb/seal: " + strconv.Itoa(e.Want) + " shares were requested but " + strconv.Itoa(e.Enrolled) + " holders are enrolled"
}

// redistribute generates a fresh key for domain, wraps rootKey under it bound
// to a newly prepared roster, seals the new shares to that roster's holders
// and persists the blob. It is the single distribution path: Shamir
// initialization, the encryption-migration mint, recovery-key generation and
// every rekey (including revocation) go through it.
//
// Ordering is deliberate: distribute FIRST, persist the blob, then commit.
// Distribute only stages the new shares, so if distribution or the persist
// fails, the previous blob -- and the previous share set, unclaimed shares
// included -- stays valid and the operation can be retried. Committed runs
// only once the blob is durable, and swaps the staged shares in before
// anything else, so the shares in force always match the blob in force.
//
// The domain key is generated straight into protected memory and never
// leaves it as an ordinary slice: the AEAD is built under a borrow, the
// distributor borrows the buffer to split it, and it is destroyed on return.
//
// expectTotal, when positive, is the share count the caller asked for; a
// different number of enrolled holders is refused rather than silently
// adopted.
func (b *StorageBarrier) redistribute(ctx context.Context, domain QuorumDomain, rootKey *secure.Buffer, threshold int, policy FactorPolicy, remove []string, expectTotal int) (*ShamirInitParams, error) {
	return b.redistributeTo(ctx, b, domain, rootKey, threshold, policy, remove, expectTotal)
}

// redistributeTo is redistribute with the blob written to target's backend
// under THIS barrier's path and AAD. The encryption migration mints its
// sibling this way so the blob it adopts is already bound to the canonical
// root key path.
//
// Redistributions of one domain are serialised (lockRedistribution): the
// distributor stages one share set per domain, so a second distribution
// interleaved with the first would replace the first's staged shares, and the
// first would then persist a blob no share in force opens.
func (b *StorageBarrier) redistributeTo(ctx context.Context, target *StorageBarrier, domain QuorumDomain, rootKey *secure.Buffer, threshold int, policy FactorPolicy, remove []string, expectTotal int) (*ShamirInitParams, error) {
	if b.distributor == nil {
		return nil, ErrCeremonyDistributorNotConfigured
	}
	release, err := b.lockRedistribution(ctx, domain)
	if err != nil {
		return nil, err
	}
	defer release()
	if !policy.Valid() {
		return nil, &InvalidRosterError{Reason: "unknown factor policy " + string(policy)}
	}
	roster, err := b.distributor.PrepareRoster(ctx, domain, threshold, policy.OrDefault(), remove)
	if err != nil {
		return nil, err
	}
	if expectTotal > 0 && roster.Total != expectTotal {
		return nil, &HolderCountMismatchError{Want: expectTotal, Enrolled: roster.Total}
	}
	distribution := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, distribution); err != nil {
		return nil, err
	}
	roster.Distribution = hex.EncodeToString(distribution)

	key, err := newUnsealKey()
	if err != nil {
		return nil, err
	}
	defer key.Destroy()

	var aead cipher.AEAD
	if err := key.WithBytes(func(k []byte) error {
		var aerr error
		aead, aerr = b.unsealKeyAEAD(k)
		return aerr
	}); err != nil {
		return nil, err
	}
	var blob, digest []byte
	if err := rootKey.WithBytes(func(rk []byte) error {
		var werr error
		blob, digest, werr = b.wrapRootKeyWith(domain, aead, rk, roster)
		return werr
	}); err != nil {
		return nil, err
	}

	count, err := b.distributor.Distribute(ctx, key, roster, digest)
	if err != nil {
		return nil, err
	}

	if err := target.base.Put(ctx, target.wrappedKeyPath(domain), blob); err != nil {
		return nil, err
	}
	// The blob is durable: the staged shares go live before anything else
	// can fail.
	if err := b.distributor.Committed(ctx, roster, remove); err != nil {
		return nil, err
	}
	if domain == DomainUnseal {
		if err := target.recordShamirParams(ctx, roster.Threshold, roster.Total); err != nil {
			return nil, err
		}
	}

	emitAudit(b.config.AuditLogger, "distribute", "barrier", b.config.RootKeyPath, "allow",
		map[string]string{"domain": string(domain), "threshold": itoa(roster.Threshold),
			"total": itoa(roster.Total), "policy": string(roster.Policy), "removed": itoa(len(remove))})

	return &ShamirInitParams{Threshold: roster.Threshold, TotalShares: roster.Total, ShareCount: count}, nil
}

// lockRedistribution waits for domain's redistribution slot and returns the
// function that releases it. The slot is a one-place channel, created on
// first use, so the zero StorageBarrier serialises correctly. A context that
// ends while waiting returns its error and holds nothing.
func (b *StorageBarrier) lockRedistribution(ctx context.Context, domain QuorumDomain) (func(), error) {
	v, _ := b.redistributions.LoadOrStore(domain, make(chan struct{}, 1))
	slot, _ := v.(chan struct{})
	release := func() { <-slot }
	select {
	case slot <- struct{}{}:
		return release, nil
	default:
	}
	select {
	case slot <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// recordShamirParams writes threshold/total into the persisted
// (key-material-free) sealed envelope and replaces the in-memory
// ShamirStrategy and config, so Status reports the share set in force.
func (b *StorageBarrier) recordShamirParams(ctx context.Context, threshold, total int) error {
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return ErrCorruptRootKey
	}
	if sealed.ShamirThreshold != threshold || sealed.ShamirTotal != total {
		sealed.ShamirThreshold = threshold
		sealed.ShamirTotal = total
		updated, err := json.Marshal(&sealed)
		if err != nil {
			return err
		}
		if err := b.base.Put(ctx, b.config.RootKeyPath, updated); err != nil {
			return err
		}
	}
	shamirStrategy, err := NewShamirStrategy(threshold, total)
	if err != nil {
		return err
	}
	b.setStrategy(StrategyShamir, shamirStrategy)
	b.updateShamirConfig(threshold, total)
	return nil
}
