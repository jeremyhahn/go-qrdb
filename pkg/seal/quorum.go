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
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// The one quorum accumulator.
//
// Every operation a share set authorizes — unseal, seal, root token, rekey
// and recovery — runs through SubmitHolderShare and the round type below.
// There is no other way to put a share toward a quorum: the unauthenticated
// string-share entry points are gone.
//
// A round counts a share only when:
//
//  1. the named holder is in the domain's roster and is not being removed by
//     the round (a revocation);
//  2. the holder has not already contributed to the round — M means M
//     distinct holders, not M shares;
//  3. the HolderVerifier accepts the holder's factor proof; and
//  4. the share's x-coordinate is the holder's own share index, so a holder
//     cannot submit somebody else's share, or two shares.
//
// The roster the round checks against is pinned (by digest) when the round
// opens, and the quorum's reconstructed key must open the blob that roster is
// bound to, so the roster the holders were checked against is exactly the
// roster the completing key authenticates.

// quorumRoundKey identifies one round.
type quorumRoundKey struct {
	domain QuorumDomain
	op     QuorumOp
}

// quorumRound is one operation's accumulating quorum.
type quorumRound struct {
	domain       QuorumDomain
	op           QuorumOp
	roster       *HolderRoster
	rosterDigest []byte
	expiresAt    time.Time

	// rekey carries the resolved parameters of an OpRekey round.
	rekey *RekeyParams

	contributors map[string]bool
	indexes      map[int]bool
	shares       []*shamir.ProtectedShare
}

// destroy wipes every accumulated share.
func (r *quorumRound) destroy() {
	for _, s := range r.shares {
		s.Destroy()
	}
	r.shares = nil
}

// info renders the round's public state.
func (r *quorumRound) info() *QuorumRoundInfo {
	info := &QuorumRoundInfo{
		Domain:    r.domain,
		Op:        r.op,
		Required:  r.roster.Threshold,
		Submitted: len(r.shares),
		ExpiresAt: r.expiresAt,
	}
	if r.rekey != nil {
		info.NewThreshold = r.rekey.Threshold
		info.Policy = r.rekey.Policy
		info.Remove = append([]string(nil), r.rekey.Remove...)
	}
	return info
}

// RekeyParams describes a redistribution. Zero fields keep the domain's
// current values. The new total is not a parameter: it is the number of
// active holders the distributor reports once Remove is excluded.
type RekeyParams struct {
	// Threshold is the new M; zero keeps the current threshold.
	Threshold int

	// Policy is the new factor policy; empty keeps the current policy.
	Policy FactorPolicy

	// Remove names holders to revoke. They cannot contribute to the round
	// and receive no share from it.
	Remove []string
}

// QuorumRoundInfo is the public state of an open round.
type QuorumRoundInfo struct {
	// Domain and Op identify the round.
	Domain QuorumDomain
	Op     QuorumOp

	// Required is M, the number of distinct holders the round needs.
	Required int

	// Submitted is how many holders have contributed.
	Submitted int

	// ExpiresAt is when the round's shares are wiped if M is not reached.
	ExpiresAt time.Time

	// NewThreshold, Policy and Remove describe an OpRekey round's result.
	NewThreshold int
	Policy       FactorPolicy
	Remove       []string
}

// HolderSubmission is one holder's share toward a quorum.
type HolderSubmission struct {
	// Domain is the key domain the share belongs to.
	Domain QuorumDomain

	// Op is the operation the share is consent to.
	Op QuorumOp

	// Holder is the submitting holder's username.
	Holder string

	// Proof authenticates Holder; see HolderVerifier.
	Proof HolderProof

	// Share is the base64 share value, exactly as the holder's envelope
	// carried it. SubmitHolderShare takes ownership and wipes it.
	Share []byte
}

// QuorumOutcome reports what a submission did.
type QuorumOutcome struct {
	// Domain and Op identify the round.
	Domain QuorumDomain
	Op     QuorumOp

	// Required is M; Submitted is how many holders have contributed.
	Required  int
	Submitted int

	// Complete is true when this submission reached M and the operation ran.
	Complete bool

	// RootToken is set when an OpRootToken round completed.
	RootToken *RootToken

	// Rekey is set when an OpRekey round completed.
	Rekey *ShamirInitParams
}

// SetHolderVerifier installs the verifier that authenticates share holders.
// Until one is installed every submission is refused.
func (b *StorageBarrier) SetHolderVerifier(v HolderVerifier) {
	b.quorumMu.Lock()
	defer b.quorumMu.Unlock()
	b.holderVerifier = v
}

// quorumTTL returns the configured round lifetime.
func (b *StorageBarrier) quorumTTL() time.Duration {
	if b.config.Shamir != nil && b.config.Shamir.QuorumTTL > 0 {
		return b.config.Shamir.QuorumTTL
	}
	return DefaultShareTTL
}

// checkOpState refuses an operation the barrier's state cannot run.
func (b *StorageBarrier) checkOpState(op QuorumOp) error {
	sealed := b.state.Load() == stateSealed
	switch op {
	case OpUnseal, OpRecover:
		if !sealed {
			return ErrAlreadyUnsealed
		}
	case OpSeal, OpRekey:
		if sealed {
			return ErrSealed
		}
	}
	return nil
}

// roundLocked returns the open round for key, discarding one that expired or
// whose roster has since been replaced. Rounds other than OpRekey open on
// demand. Must be called with quorumMu held.
func (b *StorageBarrier) roundLocked(key quorumRoundKey, roster *HolderRoster, digest []byte) (*quorumRound, error) {
	if r, ok := b.rounds[key]; ok {
		if time.Now().Before(r.expiresAt) && bytes.Equal(r.rosterDigest, digest) {
			return r, nil
		}
		r.destroy()
		delete(b.rounds, key)
	}
	if key.op == OpRekey {
		return nil, ErrQuorumRoundNotOpen
	}
	r := &quorumRound{
		domain:       key.domain,
		op:           key.op,
		roster:       roster,
		rosterDigest: digest,
		expiresAt:    time.Now().Add(b.quorumTTL()),
		contributors: make(map[string]bool),
		indexes:      make(map[int]bool),
	}
	b.rounds[key] = r
	return r, nil
}

// StartRekey opens a rekey round for domain. The round needs a fresh quorum
// of the domain's current holders — excluding any being removed — and on
// completion redistributes the key to the remaining active holders with the
// given parameters. Revoking a holder is a StartRekey that removes them.
//
// The redistribution is validated up front against the distributor, so a
// rekey that would leave fewer holders than its threshold is refused before
// any holder is asked for a share.
func (b *StorageBarrier) StartRekey(ctx context.Context, domain QuorumDomain, params RekeyParams) (*QuorumRoundInfo, error) {
	if !OpRekey.ValidIn(domain) {
		return nil, &QuorumOpInvalidError{Domain: domain, Op: OpRekey}
	}
	if err := b.checkOpState(OpRekey); err != nil {
		return nil, err
	}
	if b.distributor == nil {
		return nil, ErrCeremonyDistributorNotConfigured
	}
	_, roster, digest, err := b.loadWrapped(ctx, domain)
	if err != nil {
		return nil, err
	}

	resolved := RekeyParams{Threshold: params.Threshold, Policy: params.Policy, Remove: params.Remove}
	if resolved.Threshold == 0 {
		resolved.Threshold = roster.Threshold
	}
	if resolved.Policy == "" {
		resolved.Policy = roster.Policy
	}
	if !resolved.Policy.Valid() {
		return nil, &InvalidRosterError{Reason: "unknown factor policy " + string(resolved.Policy)}
	}
	remove := make(map[string]bool, len(resolved.Remove))
	for _, name := range resolved.Remove {
		if _, ok := roster.Holder(name); !ok {
			return nil, &HolderNotInRosterError{Holder: name}
		}
		remove[name] = true
	}
	// The current quorum must still be reachable without the removed holders.
	if remaining := roster.Total - len(remove); remaining < roster.Threshold {
		return nil, &RevocationBreaksQuorumError{Threshold: roster.Threshold, Remaining: remaining}
	}
	// And the new share set must be distributable.
	next, err := b.distributor.PrepareRoster(ctx, domain, resolved.Threshold, resolved.Policy, resolved.Remove)
	if err != nil {
		return nil, err
	}
	if resolved.Threshold < 2 || next.Total < resolved.Threshold {
		return nil, &RevocationBreaksQuorumError{Threshold: resolved.Threshold, Remaining: next.Total}
	}

	b.quorumMu.Lock()
	defer b.quorumMu.Unlock()
	key := quorumRoundKey{domain: domain, op: OpRekey}
	if err := b.refuseStaleRosterLocked(ctx, key, digest); err != nil {
		return nil, err
	}
	if r, ok := b.rounds[key]; ok {
		if time.Now().Before(r.expiresAt) && bytes.Equal(r.rosterDigest, digest) {
			return nil, ErrQuorumRoundInProgress
		}
		r.destroy()
	}
	r := &quorumRound{
		domain:       domain,
		op:           OpRekey,
		roster:       roster,
		rosterDigest: digest,
		expiresAt:    time.Now().Add(b.quorumTTL()),
		rekey:        &resolved,
		contributors: make(map[string]bool),
		indexes:      make(map[int]bool),
	}
	b.rounds[key] = r

	emitAudit(b.config.AuditLogger, "rekey-start", "barrier", b.config.RootKeyPath, "allow",
		map[string]string{"domain": string(domain), "threshold": itoa(resolved.Threshold), "removed": itoa(len(remove))})
	return r.info(), nil
}

// SubmitHolderShare counts one authenticated holder's share toward the
// domain's round for the operation, and runs the operation when the round
// reaches its threshold. sub.Share is wiped before return whatever happens.
func (b *StorageBarrier) SubmitHolderShare(ctx context.Context, sub *HolderSubmission) (*QuorumOutcome, error) {
	if sub == nil {
		return nil, ErrShamirCombineFailed
	}
	defer secure.Wipe(sub.Share)
	if !sub.Op.ValidIn(sub.Domain) {
		return nil, &QuorumOpInvalidError{Domain: sub.Domain, Op: sub.Op}
	}
	if err := b.checkOpState(sub.Op); err != nil {
		return nil, err
	}
	if b.holderVerifier == nil {
		return nil, ErrHolderVerifierNotConfigured
	}
	if err := b.holderVerifier.AdmitSubmission(ctx, sub.Domain); err != nil {
		return nil, err
	}
	_, roster, digest, err := b.loadWrapped(ctx, sub.Domain)
	if err != nil {
		return nil, err
	}
	return b.submitAgainst(ctx, sub, roster, digest)
}

// submitAgainst admits sub against roster, the domain's roster as read at
// digest, and finishes the round when it completes.
func (b *StorageBarrier) submitAgainst(ctx context.Context, sub *HolderSubmission, roster *HolderRoster, digest []byte) (*QuorumOutcome, error) {
	b.quorumMu.Lock()
	if err := b.refuseStaleRosterLocked(ctx, quorumRoundKey{domain: sub.Domain, op: sub.Op}, digest); err != nil {
		b.quorumMu.Unlock()
		return nil, err
	}
	round, outcome, err := b.admitLocked(ctx, sub, roster, digest)
	b.quorumMu.Unlock()
	if err != nil || !outcome.Complete {
		return outcome, err
	}
	// The round is detached: finishing it (an unseal, a redistribution) can
	// be slow and must not hold other submissions up.
	defer round.destroy()
	return b.finishRound(ctx, round, outcome)
}

// refuseStaleRosterLocked settles which roster is current when the round open
// for key was opened on a different roster from digest, the one the caller
// read before taking the lock. The roster is re-read: if digest is no longer
// current the caller's read is the stale one, and it is refused with
// *StaleRosterError so the newer round and the shares counted in it survive.
// Otherwise the open round is the stale one, and roundLocked replaces it as
// before. Must be called with quorumMu held.
func (b *StorageBarrier) refuseStaleRosterLocked(ctx context.Context, key quorumRoundKey, digest []byte) error {
	r, ok := b.rounds[key]
	if !ok || bytes.Equal(r.rosterDigest, digest) {
		return nil
	}
	_, _, current, err := b.loadWrapped(ctx, key.domain)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, digest) {
		return &StaleRosterError{Domain: key.domain}
	}
	return nil
}

// admitLocked runs the admission checks and adds the share. When the round
// reaches its threshold it is detached and returned for finishing. Must be
// called with quorumMu held.
//
// The proof is verified FIRST. The sealed window admits submissions
// anonymously, so nothing about the holder or the round may be revealed to a
// caller who cannot authenticate as the holder: a name outside the roster, a
// holder being revoked and a holder who has already submitted all answer an
// unverified proof exactly as any holder does, with *HolderAuthenticationError
// carrying the verifier's error. Only a proof that verifies learns that its
// holder is revoked or has already submitted.
func (b *StorageBarrier) admitLocked(ctx context.Context, sub *HolderSubmission, roster *HolderRoster, digest []byte) (*quorumRound, *QuorumOutcome, error) {
	if b.holderVerifier == nil {
		return nil, nil, ErrHolderVerifierNotConfigured
	}
	key := quorumRoundKey{domain: sub.Domain, op: sub.Op}
	round, err := b.roundLocked(key, roster, digest)
	if err != nil {
		return nil, nil, err
	}
	if err := b.holderVerifier.VerifyHolder(ctx, sub.Domain, sub.Op, sub.Holder, round.roster, round.rosterDigest, sub.Proof); err != nil {
		b.logger.Warn("share holder authentication refused",
			slog.String("domain", string(sub.Domain)), slog.String("op", string(sub.Op)),
			slog.String("holder", sub.Holder), slog.String("error", err.Error()))
		emitAudit(b.config.AuditLogger, "quorum-share", "barrier", b.config.RootKeyPath, "deny",
			map[string]string{"domain": string(sub.Domain), "op": string(sub.Op), "holder": sub.Holder})
		return nil, nil, &HolderAuthenticationError{Holder: sub.Holder, Err: err}
	}
	holder, ok := round.roster.Holder(sub.Holder)
	if !ok {
		return nil, nil, &HolderNotInRosterError{Holder: sub.Holder}
	}
	if round.rekey != nil {
		for _, name := range round.rekey.Remove {
			if name == sub.Holder {
				return nil, nil, &HolderRevokedError{Holder: sub.Holder}
			}
		}
	}
	if round.contributors[sub.Holder] {
		return nil, nil, &HolderAlreadySubmittedError{Holder: sub.Holder}
	}

	// NewProtectedShare wipes the value on success and leaves it on a
	// validation failure; the deferred wipe in SubmitHolderShare covers both.
	// BOUNDED EXCEPTION to server.secure_memory_budget_mib: go-quicraft's
	// NewProtectedShare allocates outside the process budget (go-quicraft#204).
	// Here it is reachable only by an authenticated holder, once per holder
	// per round, so at most Total enclaves per round.
	share, err := shamir.NewProtectedShare(holder.ShareIndex, round.roster.Threshold, round.roster.Total, sub.Share)
	if err != nil {
		return nil, nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "protect share", Cause: err}
	}
	x, err := shareXCoordinate(share)
	if err != nil || x != holder.ShareIndex || round.indexes[x] {
		share.Destroy()
		return nil, nil, &ShareIndexMismatchError{Holder: sub.Holder, Want: holder.ShareIndex}
	}

	round.contributors[sub.Holder] = true
	round.indexes[x] = true
	round.shares = append(round.shares, share)

	outcome := &QuorumOutcome{
		Domain:    sub.Domain,
		Op:        sub.Op,
		Required:  round.roster.Threshold,
		Submitted: len(round.shares),
	}
	emitAudit(b.config.AuditLogger, "quorum-share", "barrier", b.config.RootKeyPath, "allow",
		map[string]string{"domain": string(sub.Domain), "op": string(sub.Op), "holder": sub.Holder,
			"submitted": itoa(outcome.Submitted), "required": itoa(outcome.Required)})
	if outcome.Submitted < outcome.Required {
		return round, outcome, nil
	}
	outcome.Complete = true
	delete(b.rounds, key)
	return round, outcome, nil
}

// shareXCoordinate reads a protected share's x-coordinate. The value is the
// base64 of [version][x][y...]; only the first four base64 characters are
// decoded, into a buffer wiped before return.
func shareXCoordinate(share *shamir.ProtectedShare) (int, error) {
	x := -1
	err := share.WithValue(func(v []byte) error {
		if len(v) < 4 {
			return ErrShamirCombineFailed
		}
		var head [3]byte
		defer secure.Wipe(head[:])
		if _, err := base64.StdEncoding.Decode(head[:], v[:4]); err != nil {
			return ErrShamirCombineFailed
		}
		x = int(head[1])
		return nil
	})
	return x, err
}

// finishRound reconstructs the round's key, authenticates it (and the pinned
// roster) by opening the domain's wrapped blob, and runs the operation.
func (b *StorageBarrier) finishRound(ctx context.Context, round *quorumRound, outcome *QuorumOutcome) (*QuorumOutcome, error) {
	secretEnclave, err := shamir.CombineProtected(round.shares)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Cause: err}
	}
	defer secretEnclave.Destroy()
	round.destroy()

	keyBuf, err := copyEnclaveToBuffer(secretEnclave)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrShamirCombineFailed, Op: "read reconstructed key", Cause: err}
	}
	defer keyBuf.Destroy()

	// If the domain was redistributed while this round was open, its shares
	// reconstruct a key that no longer exists and the unwrap below fails with
	// ErrUnsealKeyMismatch: every distribution has a fresh key and a unique
	// roster digest, so no extra check is needed here.
	//
	// The key cannot be read under a held borrow: resolveDomainKey hands it to
	// the configured AEADProvider, an interface consumers implement, and then
	// decrypts into a second protected buffer of its own.
	//secure:unborrowed injected-interface resolveDomainKey hands the key to the configured AEADProvider (go-qrdb#428)
	rootKey, err := b.resolveDomainKey(ctx, round.domain, keyBuf.Bytes())
	if err != nil {
		emitAudit(b.config.AuditLogger, "quorum-complete", "barrier", b.config.RootKeyPath, "deny",
			map[string]string{"domain": string(round.domain), "op": string(round.op)})
		return nil, err
	}
	defer rootKey.Destroy()

	if err := quorumFinishers[round.op](ctx, b, round, rootKey, outcome); err != nil {
		return nil, err
	}
	emitAudit(b.config.AuditLogger, "quorum-complete", "barrier", b.config.RootKeyPath, "allow",
		map[string]string{"domain": string(round.domain), "op": string(round.op),
			"submitted": itoa(outcome.Submitted), "required": itoa(outcome.Required)})
	b.logger.Info("share-holder quorum complete",
		slog.String("domain", string(round.domain)), slog.String("op", string(round.op)),
		slog.Int("holders", outcome.Submitted))
	return outcome, nil
}

// quorumFinisher runs a completed round's operation with the authenticated
// root key, which it must not retain.
type quorumFinisher func(ctx context.Context, b *StorageBarrier, round *quorumRound, rootKey *secure.Buffer, outcome *QuorumOutcome) error

// quorumFinishers dispatches a completed round to its operation.
var quorumFinishers = map[QuorumOp]quorumFinisher{
	OpUnseal: func(ctx context.Context, b *StorageBarrier, _ *quorumRound, rootKey *secure.Buffer, _ *QuorumOutcome) error {
		return b.unsealFromRootKey(ctx, rootKey, StrategyShamir)
	},
	OpRecover: func(ctx context.Context, b *StorageBarrier, _ *quorumRound, rootKey *secure.Buffer, _ *QuorumOutcome) error {
		return b.unsealFromRootKey(ctx, rootKey, StrategyID("recovery"))
	},
	OpSeal: func(_ context.Context, b *StorageBarrier, _ *quorumRound, _ *secure.Buffer, _ *QuorumOutcome) error {
		return b.Seal()
	},
	OpRootToken: func(_ context.Context, _ *StorageBarrier, _ *quorumRound, rootKey *secure.Buffer, outcome *QuorumOutcome) error {
		token, err := deriveRootToken(rootKey)
		if err != nil {
			return err
		}
		outcome.RootToken = token
		return nil
	},
	OpRekey: func(ctx context.Context, b *StorageBarrier, round *quorumRound, rootKey *secure.Buffer, outcome *QuorumOutcome) error {
		params, err := b.redistribute(ctx, round.domain, rootKey, round.rekey.Threshold, round.rekey.Policy, round.rekey.Remove, 0)
		if err != nil {
			return err
		}
		outcome.Rekey = params
		return nil
	},
}

// newQuorumRounds returns the empty round table.
func newQuorumRounds() map[quorumRoundKey]*quorumRound {
	return make(map[quorumRoundKey]*quorumRound)
}

// quorumState is embedded in StorageBarrier.
type quorumState struct {
	quorumMu       sync.Mutex
	rounds         map[quorumRoundKey]*quorumRound
	holderVerifier HolderVerifier
	// redistributions holds one redistribution slot per domain
	// (QuorumDomain -> chan struct{}); see lockRedistribution.
	redistributions sync.Map
}
