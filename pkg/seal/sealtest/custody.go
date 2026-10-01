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

// Custody doubles. These are faithful in-memory implementations of the two
// custody seams pkg/seal defines — seal.CeremonyDistributor and
// seal.HolderVerifier — for tests outside pkg/seal that need a working Shamir
// barrier without the full ceremony stack.
//
// They keep the contracts the real implementation (pkg/ceremony) keeps:
// rosters list every active holder minus the excluded ones with share indexes
// 1..N in a stable order, shares are split with the real Shamir library, a
// proof answers only the challenge issued for exactly its (domain, op,
// holder, roster digest), and a challenge is consumed by its first
// verification whether it succeeds or not.
//
// pkg/seal's own tests cannot import this package (the import would be a
// cycle), so they carry an in-package twin in test_distributor_test.go. The
// two must keep the same contract.

package sealtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"

	quicraftshamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ErrTooFewHolders is what Distributor answers when the active holder set
// cannot carry the requested threshold.
var ErrTooFewHolders = errors.New("sealtest: too few holders for the threshold")

// ErrProof is what Verifier answers for a proof that does not answer a
// challenge it issued.
var ErrProof = errors.New("sealtest: proof does not answer an issued challenge")

// ErrNothingStaged is returned by Distributor.Committed when no shares are
// staged for the committed roster's distribution.
var ErrNothingStaged = errors.New("sealtest: no shares staged for the committed distribution")

// Distributor is an in-memory seal.CeremonyDistributor that captures each
// holder's share value so a test can submit it.
type Distributor struct {
	mu      sync.Mutex
	holders map[seal.QuorumDomain][]string
	shares  map[seal.QuorumDomain]map[string]string
	// staged holds what Distribute sealed until Committed for the same
	// roster swaps it in, as the real distributor does.
	staged map[seal.QuorumDomain]staged
}

// staged is one staged distribution.
type staged struct {
	distribution string
	shares       map[string]string
}

var _ seal.CeremonyDistributor = (*Distributor)(nil)

// NewDistributor returns a Distributor whose unseal domain has holders.
func NewDistributor(holders ...string) *Distributor {
	return &Distributor{
		holders: map[seal.QuorumDomain][]string{seal.DomainUnseal: append([]string(nil), holders...)},
		shares:  map[seal.QuorumDomain]map[string]string{},
		staged:  map[seal.QuorumDomain]staged{},
	}
}

// SetHolders replaces domain's active holder set.
func (d *Distributor) SetHolders(domain seal.QuorumDomain, holders ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.holders[domain] = append([]string(nil), holders...)
}

// Share returns the share value last distributed to holder in domain, or ""
// when it holds none.
func (d *Distributor) Share(domain seal.QuorumDomain, holder string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.shares[domain][holder]
}

// PrepareRoster implements seal.CeremonyDistributor.
func (d *Distributor) PrepareRoster(_ context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	active := make([]string, 0, len(d.holders[domain]))
	for _, h := range d.holders[domain] {
		if !skip[h] {
			active = append(active, h)
		}
	}
	sort.Strings(active)
	if threshold < 2 || len(active) < threshold {
		return nil, ErrTooFewHolders
	}
	r := seal.NewHolderRoster(domain, policy, threshold)
	for i, h := range active {
		r.Holders = append(r.Holders, seal.RosterHolder{
			Username:       h,
			Role:           seal.RoleCustodian,
			ShareIndex:     i + 1,
			CertThumbprint: hex.EncodeToString([]byte("cert-" + h)),
			Designated:     "mtls",
			Factors:        []seal.RosterFactor{{Name: "mtls", EnrollmentID: "mtls:" + h, PublicKey: []byte("pk-" + h)}},
		})
	}
	r.Total = len(r.Holders)
	return r, nil
}

// Distribute implements seal.CeremonyDistributor.
func (d *Distributor) Distribute(_ context.Context, secret *secure.Buffer, roster *seal.HolderRoster, _ []byte) (int, error) {
	// SplitBytes returns each share's wire value (the bytes of Share.Value),
	// in index order. The double keeps them as strings because a test reads
	// them back to submit; the split buffers themselves are wiped here.
	var values [][]byte
	if err := secret.WithBytes(func(k []byte) error {
		var serr error
		values, serr = quicraftshamir.SplitBytes(k, roster.Threshold, roster.Total)
		return serr
	}); err != nil {
		return 0, err
	}
	defer func() {
		for _, v := range values {
			secure.Wipe(v)
		}
	}()
	byHolder := make(map[string]string, len(values))
	for _, h := range roster.Holders {
		byHolder[h.Username] = string(values[h.ShareIndex-1])
	}
	d.mu.Lock()
	d.staged[roster.Domain] = staged{distribution: roster.Distribution, shares: byHolder}
	d.mu.Unlock()
	return len(values), nil
}

// Committed implements seal.CeremonyDistributor: the shares staged for
// roster go live and removed holders stop being holders of the domain. As
// pkg/ceremony does, it refuses with ErrNothingStaged, changing nothing, when
// no shares are staged for roster's distribution.
func (d *Distributor) Committed(_ context.Context, roster *seal.HolderRoster, removed []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.staged[roster.Domain]
	if !ok || s.distribution != roster.Distribution {
		return ErrNothingStaged
	}
	d.shares[roster.Domain] = s.shares
	delete(d.staged, roster.Domain)
	gone := make(map[string]bool, len(removed))
	for _, r := range removed {
		gone[r] = true
	}
	kept := d.holders[roster.Domain][:0]
	for _, h := range d.holders[roster.Domain] {
		if !gone[h] {
			kept = append(kept, h)
		}
	}
	d.holders[roster.Domain] = kept
	return nil
}

// challenge is one challenge Verifier issued.
type challenge struct {
	domain seal.QuorumDomain
	op     seal.QuorumOp
	holder string
	digest string
	answer string
}

// Verifier is an in-memory seal.HolderVerifier.
type Verifier struct {
	mu     sync.Mutex
	issued map[string]*challenge
}

var _ seal.HolderVerifier = (*Verifier)(nil)

// NewVerifier returns a Verifier with no challenges outstanding.
func NewVerifier() *Verifier {
	return &Verifier{issued: map[string]*challenge{}}
}

// Issue records a challenge for (domain, op, holder, digest) and returns the
// proof that answers it.
func (v *Verifier) Issue(domain seal.QuorumDomain, op seal.QuorumOp, holder string, digest []byte) seal.HolderProof {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	answer := make([]byte, 16)
	_, _ = rand.Read(answer)
	cid := hex.EncodeToString(id)
	v.mu.Lock()
	v.issued[cid] = &challenge{domain: domain, op: op, holder: holder, digest: hex.EncodeToString(digest), answer: hex.EncodeToString(answer)}
	v.mu.Unlock()
	return seal.HolderProof{ChallengeID: cid, Response: []byte(hex.EncodeToString(answer))}
}

// AdmitSubmission implements seal.HolderVerifier. The double throttles
// nothing: it admits every submission to be examined.
func (v *Verifier) AdmitSubmission(context.Context, seal.QuorumDomain) error { return nil }

// VerifyHolder implements seal.HolderVerifier.
func (v *Verifier) VerifyHolder(_ context.Context, domain seal.QuorumDomain, op seal.QuorumOp, holder string, roster *seal.HolderRoster, digest []byte, proof seal.HolderProof) error {
	v.mu.Lock()
	ch, ok := v.issued[proof.ChallengeID]
	delete(v.issued, proof.ChallengeID)
	v.mu.Unlock()
	if !ok || ch.domain != domain || ch.op != op || ch.holder != holder || ch.digest != hex.EncodeToString(digest) || ch.answer != string(proof.Response) {
		return ErrProof
	}
	_, err := roster.AdmittedFactor(holder, proof.Factor)
	return err
}

// Custody wires a Distributor and a Verifier onto barriers and drives quorums
// through them.
type Custody struct {
	Distributor *Distributor
	Verifier    *Verifier
}

// NewCustody returns a Custody whose unseal domain has holders.
func NewCustody(holders ...string) *Custody {
	return &Custody{Distributor: NewDistributor(holders...), Verifier: NewVerifier()}
}

// Install sets c's distributor and verifier on b.
func (c *Custody) Install(b *seal.StorageBarrier) {
	b.SetCeremonyDistributor(c.Distributor)
	b.SetHolderVerifier(c.Verifier)
}

// Quorum submits each holder's distributed share to (domain, op) on b, each
// with a proof answering a freshly issued challenge, and returns the last
// outcome.
func (c *Custody) Quorum(ctx context.Context, b *seal.StorageBarrier, domain seal.QuorumDomain, op seal.QuorumOp, holders ...string) (*seal.QuorumOutcome, error) {
	_, digest, err := b.HolderRoster(ctx, domain)
	if err != nil {
		return nil, err
	}
	var out *seal.QuorumOutcome
	for _, h := range holders {
		out, err = b.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: domain,
			Op:     op,
			Holder: h,
			Proof:  c.Verifier.Issue(domain, op, h, digest),
			Share:  []byte(c.Distributor.Share(domain, h)),
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
