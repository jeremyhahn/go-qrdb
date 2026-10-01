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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// errTestTooFewHolders is what the test distributor answers when the active
// holder set cannot carry the requested threshold, as the real distributor
// (pkg/ceremony) does with its own typed error.
var errTestTooFewHolders = errors.New("test distributor: too few holders for the threshold")

// errTestNothingStaged is the capture distributor's refusal of a commit with
// no shares staged for its distribution.
var errTestNothingStaged = errors.New("test distributor: no shares staged for the committed distribution")

// captureDistributor is a faithful in-memory CeremonyDistributor. It keeps a
// holder set per domain, prepares rosters the way pkg/ceremony does (every
// active holder, minus the excluded ones, share indexes 1..N in a stable
// order), splits with the real Shamir library, captures each holder's share
// value so a test can submit it, and drops removed holders on Committed.
type captureDistributor struct {
	mu        sync.Mutex
	holders   map[QuorumDomain][]string
	shares    map[QuorumDomain]map[string]string
	digests   map[QuorumDomain][]byte
	committed map[QuorumDomain]*HolderRoster
	failNext  error
	// staged holds what Distribute sealed until Committed for the same
	// roster swaps it in, as the real distributor does.
	staged map[QuorumDomain]*stagedCapture
}

// stagedCapture is one staged distribution.
type stagedCapture struct {
	distribution string
	shares       map[string]string
	digest       []byte
}

// newCaptureDistributor returns a distributor whose unseal domain has the
// given holders.
func newCaptureDistributor(holders ...string) *captureDistributor {
	return &captureDistributor{
		holders:   map[QuorumDomain][]string{DomainUnseal: append([]string(nil), holders...)},
		shares:    map[QuorumDomain]map[string]string{},
		digests:   map[QuorumDomain][]byte{},
		committed: map[QuorumDomain]*HolderRoster{},
		staged:    map[QuorumDomain]*stagedCapture{},
	}
}

// setHolders replaces a domain's active holder set.
func (c *captureDistributor) setHolders(domain QuorumDomain, holders ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holders[domain] = append([]string(nil), holders...)
}

// testThumb is the deterministic certificate thumbprint of a test holder.
func testThumb(holder string) string {
	return hex.EncodeToString([]byte("cert-" + holder))
}

// PrepareRoster implements CeremonyDistributor.
func (c *captureDistributor) PrepareRoster(_ context.Context, domain QuorumDomain, threshold int, policy FactorPolicy, exclude []string) (*HolderRoster, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[e] = true
	}
	active := make([]string, 0, len(c.holders[domain]))
	for _, h := range c.holders[domain] {
		if !skip[h] {
			active = append(active, h)
		}
	}
	sort.Strings(active)
	if threshold < 2 || len(active) < threshold {
		return nil, errTestTooFewHolders
	}
	r := NewHolderRoster(domain, policy, threshold)
	r.Distribution = "prepared"
	for i, h := range active {
		r.Holders = append(r.Holders, RosterHolder{
			Username:       h,
			Role:           RoleCustodian,
			ShareIndex:     i + 1,
			CertThumbprint: testThumb(h),
			Designated:     "mtls",
			Factors: []RosterFactor{
				{Name: "mtls", EnrollmentID: "mtls:" + h, PublicKey: []byte("pk-" + h)},
				{Name: "fido2", EnrollmentID: "fido2:" + h, PublicKey: []byte("fk-" + h)},
			},
		})
	}
	r.Total = len(r.Holders)
	return r, nil
}

// Distribute implements CeremonyDistributor.
func (c *captureDistributor) Distribute(_ context.Context, secret *secure.Buffer, roster *HolderRoster, digest []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.failNext; err != nil {
		c.failNext = nil
		return 0, err
	}
	var values [][]byte
	if err := secret.WithBytes(func(k []byte) error {
		var serr error
		values, serr = quicraftShamir.SplitBytes(k, roster.Threshold, roster.Total)
		return serr
	}); err != nil {
		return 0, err
	}
	byHolder := make(map[string]string, len(values))
	for _, h := range roster.Holders {
		byHolder[h.Username] = string(values[h.ShareIndex-1])
	}
	for _, v := range values {
		clear(v)
	}
	c.staged[roster.Domain] = &stagedCapture{distribution: roster.Distribution, shares: byHolder, digest: append([]byte(nil), digest...)}
	return len(values), nil
}

// Committed implements CeremonyDistributor.
//
// As pkg/ceremony does, it refuses with errTestNothingStaged, changing
// nothing, when no shares are staged for roster's distribution.
func (c *captureDistributor) Committed(_ context.Context, roster *HolderRoster, removed []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.staged[roster.Domain]
	if s == nil || s.distribution != roster.Distribution {
		return errTestNothingStaged
	}
	c.shares[roster.Domain] = s.shares
	c.digests[roster.Domain] = s.digest
	delete(c.staged, roster.Domain)
	gone := map[string]bool{}
	for _, r := range removed {
		gone[r] = true
	}
	kept := c.holders[roster.Domain][:0]
	for _, h := range c.holders[roster.Domain] {
		if !gone[h] {
			kept = append(kept, h)
		}
	}
	c.holders[roster.Domain] = kept
	c.committed[roster.Domain] = roster
	return nil
}

// share returns the share value distributed to holder in domain.
func (c *captureDistributor) share(domain QuorumDomain, holder string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shares[domain][holder]
}

// testChallenge is one challenge the test verifier issued.
type testChallenge struct {
	domain QuorumDomain
	op     QuorumOp
	holder string
	digest string
	answer string
}

// testVerifier is a faithful HolderVerifier: it answers only a proof whose
// challenge it issued for exactly (domain, op, holder, roster digest), with
// the expected response, and consumes the challenge whether or not the proof
// verifies, as the contract requires.
type testVerifier struct {
	mu     sync.Mutex
	issued map[string]*testChallenge
	calls  int
}

func newTestVerifier() *testVerifier {
	return &testVerifier{issued: map[string]*testChallenge{}}
}

// issue records a challenge and returns the proof that answers it.
func (v *testVerifier) issue(domain QuorumDomain, op QuorumOp, holder string, digest []byte) HolderProof {
	v.mu.Lock()
	defer v.mu.Unlock()
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	answer := make([]byte, 16)
	_, _ = rand.Read(answer)
	cid := hex.EncodeToString(id)
	v.issued[cid] = &testChallenge{domain: domain, op: op, holder: holder, digest: hex.EncodeToString(digest), answer: hex.EncodeToString(answer)}
	return HolderProof{ChallengeID: cid, Response: []byte(hex.EncodeToString(answer))}
}

var errTestProof = errors.New("test verifier: proof does not answer an issued challenge")

// AdmitSubmission implements seal.HolderVerifier. The double throttles
// nothing: it admits every submission to be examined.
func (v *testVerifier) AdmitSubmission(context.Context, QuorumDomain) error { return nil }

// VerifyHolder implements HolderVerifier.
func (v *testVerifier) VerifyHolder(_ context.Context, domain QuorumDomain, op QuorumOp, holder string, roster *HolderRoster, digest []byte, proof HolderProof) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	ch, ok := v.issued[proof.ChallengeID]
	delete(v.issued, proof.ChallengeID)
	if !ok || ch.domain != domain || ch.op != op || ch.holder != holder || ch.digest != hex.EncodeToString(digest) || ch.answer != string(proof.Response) {
		return errTestProof
	}
	if _, err := roster.AdmittedFactor(holder, proof.Factor); err != nil {
		return err
	}
	return nil
}

var _ HolderVerifier = (*testVerifier)(nil)
var _ CeremonyDistributor = (*captureDistributor)(nil)

// holderFixture is an initialized Shamir barrier with holders, a
// distributor and a verifier wired.
type holderFixture struct {
	t        testing.TB
	b        *StorageBarrier
	dist     *captureDistributor
	verifier *testVerifier
}

// holderNames returns h1..hN.
func holderNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "h" + strconv.Itoa(i+1)
	}
	return out
}

// newHolderFixture initializes a threshold-of-total Shamir barrier over
// in-memory storage, distributing to holders h1..h<total>. The barrier is
// left unsealed, as initialization leaves it.
func newHolderFixture(t testing.TB, threshold, total int) *holderFixture {
	t.Helper()
	shamirStrategy, err := NewShamirStrategy(threshold, total)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &ShamirConfig{Threshold: threshold, TotalShares: total},
	}, shamirStrategy)
	if err != nil {
		t.Fatal(err)
	}
	f := &holderFixture{t: t, b: b, dist: newCaptureDistributor(holderNames(total)...), verifier: newTestVerifier()}
	b.SetCeremonyDistributor(f.dist)
	b.SetHolderVerifier(f.verifier)
	if _, err := b.InitializeShamir(context.Background(), ""); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	return f
}

// submit sends holder's own distributed share, with a valid proof, to
// (domain, op).
func (f *holderFixture) submit(domain QuorumDomain, op QuorumOp, holder string) (*QuorumOutcome, error) {
	f.t.Helper()
	return f.submitShare(domain, op, holder, f.dist.share(domain, holder))
}

// submitShare sends an arbitrary share value as holder, with a valid proof.
func (f *holderFixture) submitShare(domain QuorumDomain, op QuorumOp, holder, share string) (*QuorumOutcome, error) {
	f.t.Helper()
	_, digest, err := f.b.HolderRoster(context.Background(), domain)
	if err != nil {
		return nil, err
	}
	return f.b.SubmitHolderShare(context.Background(), &HolderSubmission{
		Domain: domain,
		Op:     op,
		Holder: holder,
		Proof:  f.verifier.issue(domain, op, holder, digest),
		Share:  []byte(share),
	})
}

// quorum submits holders' shares in order and returns the last outcome.
func (f *holderFixture) quorum(domain QuorumDomain, op QuorumOp, holders ...string) *QuorumOutcome {
	f.t.Helper()
	var out *QuorumOutcome
	for _, h := range holders {
		o, err := f.submit(domain, op, h)
		if err != nil {
			f.t.Fatalf("%s %s by %s: %v", domain, op, h, err)
		}
		out = o
	}
	return out
}

// seal seals the barrier through a seal quorum of the first threshold holders.
func (f *holderFixture) sealWith(holders ...string) {
	f.t.Helper()
	if o := f.quorum(DomainUnseal, OpSeal, holders...); !o.Complete {
		f.t.Fatalf("seal quorum incomplete: %+v", o)
	}
	if !f.b.IsSealed() {
		f.t.Fatal("barrier not sealed after seal quorum")
	}
}
