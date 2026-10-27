// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"crypto/rand"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	// SignedInitTTL is how long a statement TenantBarrierInitBegin issued
	// waits for TenantBarrierInitComplete. It leaves the administrator time
	// to sign on a hardware token, and no longer.
	SignedInitTTL = 5 * time.Minute

	// MaxOutstandingSignedInits bounds the statements a node holds unredeemed,
	// so begin cannot be used to grow its memory without limit.
	MaxOutstandingSignedInits = 1024

	// SignedInitReferenceSize is the length in bytes of a statement
	// reference: 256 random bits, unguessable and never reissued.
	SignedInitReferenceSize = 32
)

// TenantBarrierInitBeginRequest asks for the init statement a tenant
// barrier's administrator signs away from the node: the first half of a
// two-phase signed initialization, for a client that holds its key behind a
// transport rather than a crypto.Signer in this process.
type TenantBarrierInitBeginRequest struct {
	// TenantID is the tenant whose barrier is initialized.
	TenantID string
	// Strategy is the sealing strategy; empty is passphrase.
	Strategy seal.StrategyID
	// KeyRef is the strategy's reference to the tenant's key; see
	// WithBarrierKeyRef.
	KeyRef string
	// Threshold and Shares are a Shamir init's split; zero otherwise.
	Threshold int
	Shares    int
	// Roster is a Shamir init's inline officer roster; nil otherwise.
	Roster *TenantBarrierShamirRoster
	// SignerPublicKey is the DER SubjectPublicKeyInfo of the key that signs
	// the statement: the tenant's pending first admin's.
	SignerPublicKey []byte
}

// TenantBarrierInitBeginResult is the statement to sign and how to redeem it.
type TenantBarrierInitBeginResult struct {
	// TenantID is the tenant whose barrier the statement initializes.
	TenantID string `json:"tenant_id"`
	// Strategy is the strategy the statement names.
	Strategy string `json:"strategy"`
	// Reference names the statement to TenantBarrierInitComplete. It is
	// single-use and lapses at ExpiresAt.
	Reference []byte `json:"reference"`
	// Statement is the exact message the administrator signs, detached: ECDSA
	// over its SHA-256 digest in ASN.1 form, RSA PKCS #1 v1.5 over its SHA-256
	// digest, or pure Ed25519 over the bytes themselves.
	Statement []byte `json:"statement"`
	// ExpiresAt is when Reference lapses.
	ExpiresAt time.Time `json:"expires_at"`
	// Threshold, TotalShares, ShareCount and Holders report a Shamir init's
	// prepared split, as the first Shamir request of TenantBarrierInit does.
	// Zero for every other strategy.
	Threshold   int                       `json:"threshold,omitempty"`
	TotalShares int                       `json:"total_shares,omitempty"`
	ShareCount  int                       `json:"share_count,omitempty"`
	Holders     []TenantBarrierHolderCert `json:"holders,omitempty"`
}

// TenantBarrierInitCompleteRequest redeems a begun statement with the
// administrator's detached signature over it.
type TenantBarrierInitCompleteRequest struct {
	// TenantID is the tenant the statement was begun for.
	TenantID string
	// Reference is TenantBarrierInitBeginResult.Reference.
	Reference []byte
	// Signature is the detached signature over
	// TenantBarrierInitBeginResult.Statement.
	Signature []byte
	// Passphrase is the passphrase strategy's credential in protected
	// memory, borrowed for the call: the caller owns and destroys it. Nil
	// for every other strategy.
	Passphrase *secure.Buffer
}

// NewSignedInitPassphrase moves a transport's decoded passphrase into
// protected memory for TenantBarrierInitCompleteRequest.Passphrase, wiping p
// whether or not it succeeds. An empty p, the request of every strategy but
// passphrase, yields a nil Buffer. The caller owns the result and destroys it;
// Destroy is safe on nil.
func NewSignedInitPassphrase(p []byte) (*secure.Buffer, error) {
	if len(p) == 0 {
		return nil, nil
	}
	return secure.NewBufferFromBytes(p)
}

// SignedInitReason names why a statement reference was refused. The values
// are stable: they reach the caller in the error text and the audit trail.
type SignedInitReason string

const (
	// SignedInitUnknown: the reference was never issued on this node, was
	// issued for another tenant, or was already redeemed -- a replay.
	SignedInitUnknown SignedInitReason = "statement reference unknown or already used"
	// SignedInitExpired: the reference outlived SignedInitTTL.
	SignedInitExpired SignedInitReason = "statement reference expired"
)

// SignedInitError is returned by TenantBarrierInitComplete for a statement
// reference that cannot be redeemed.
type SignedInitError struct {
	TenantID string
	Reason   SignedInitReason
}

func (e *SignedInitError) Error() string {
	return "tenant: signed barrier init for " + strconv.Quote(e.TenantID) + ": " + string(e.Reason)
}

// SignedInitFieldRequiredError is returned by TenantBarrierInitBegin and
// TenantBarrierInitComplete for a request missing Field.
type SignedInitFieldRequiredError struct {
	Field string
}

func (e *SignedInitFieldRequiredError) Error() string {
	return "tenant: signed barrier init: " + e.Field + " is required"
}

// TooManySignedInitsError is returned by TenantBarrierInitBegin when the node
// already holds Limit unexpired statements. Retrying after SignedInitTTL
// succeeds.
type TooManySignedInitsError struct {
	Limit int
}

func (e *TooManySignedInitsError) Error() string {
	return "tenant: " + strconv.Itoa(e.Limit) + " signed barrier inits already outstanding"
}

// SignedInitUnsupportedError is returned by TenantBarrierInitBegin on a node
// composed without a BarrierInitBook (WithBarrierInitBook): it claims no
// statement, so there is nothing for a signature to admit.
type SignedInitUnsupportedError struct {
	TenantID string
}

func (e *SignedInitUnsupportedError) Error() string {
	return "tenant: signed barrier init for " + strconv.Quote(e.TenantID) + " is not supported: this node records no init statements"
}

// signedInit is one begun statement awaiting its signature.
type signedInit struct {
	tenantID  string
	strategy  seal.StrategyID
	keyRef    string
	threshold int
	shares    int
	// prepared is a Shamir init's split, which the completing request keys
	// the barrier with; nil for every other strategy.
	prepared  *seal.ShamirInitParams
	statement []byte
	expiresAt time.Time
}

// signedInitBook holds the statements this node issued and has not seen
// redeemed. A statement lives with the node that prepared it, in memory, like
// the init challenge: a Shamir statement signs a split only this node holds.
//
// Its zero value is ready for use, and it takes no lock: redeeming a
// reference is a single LoadAndDelete, so two completions racing on one
// reference cannot both succeed.
type signedInitBook struct {
	// now is the clock; nil is time.Now.
	now     func() time.Time
	entries sync.Map // string(reference) -> *signedInit
	count   atomic.Int64
}

func (b *signedInitBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// issue records e under a fresh reference and returns the reference.
func (b *signedInitBook) issue(e *signedInit) ([]byte, error) {
	if !b.reserve() {
		b.sweep()
		if !b.reserve() {
			return nil, &TooManySignedInitsError{Limit: MaxOutstandingSignedInits}
		}
	}
	ref := make([]byte, SignedInitReferenceSize)
	_, _ = rand.Read(ref) // crypto/rand.Read never fails (Go 1.24+).
	b.entries.Store(string(ref), e)
	return ref, nil
}

// redeem spends ref for tenantID. The reference is spent by the attempt
// whatever its outcome, so a second attempt -- a replay -- is unknown.
func (b *signedInitBook) redeem(tenantID string, ref []byte) (*signedInit, error) {
	v, ok := b.entries.LoadAndDelete(string(ref))
	if !ok {
		return nil, &SignedInitError{TenantID: tenantID, Reason: SignedInitUnknown}
	}
	b.count.Add(-1)
	e, ok := v.(*signedInit)
	if !ok || e.tenantID != tenantID {
		return nil, &SignedInitError{TenantID: tenantID, Reason: SignedInitUnknown}
	}
	if b.clock().After(e.expiresAt) {
		return nil, &SignedInitError{TenantID: tenantID, Reason: SignedInitExpired}
	}
	return e, nil
}

// reserve claims one slot under MaxOutstandingSignedInits.
func (b *signedInitBook) reserve() bool {
	for {
		n := b.count.Load()
		if n >= MaxOutstandingSignedInits {
			return false
		}
		if b.count.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// sweep drops every expired statement, releasing its slot.
func (b *signedInitBook) sweep() {
	now := b.clock()
	b.entries.Range(func(k, v any) bool {
		if e, ok := v.(*signedInit); !ok || now.After(e.expiresAt) {
			if _, loaded := b.entries.LoadAndDelete(k); loaded {
				b.count.Add(-1)
			}
		}
		return true
	})
}

// TenantBarrierInitBegin prepares req.TenantID's epoch-1 init statement naming
// req.SignerPublicKey as its signer and returns the message that key signs,
// with a single-use reference TenantBarrierInitComplete redeems. A Shamir init
// prepares its split here, as TenantBarrierInit's first Shamir request does,
// so the statement signs that split. Nothing is claimed and no key exists
// until the statement is completed.
func (s *TenantService) TenantBarrierInitBegin(ctx context.Context, req *TenantBarrierInitBeginRequest) (*TenantBarrierInitBeginResult, error) {
	if req == nil || req.TenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if len(req.SignerPublicKey) == 0 {
		return nil, &SignedInitFieldRequiredError{Field: "signer public key"}
	}
	if err := enforceTenantScope(ctx, req.TenantID, "initialize"); err != nil {
		return nil, err
	}
	if s.initBook == nil {
		return nil, &SignedInitUnsupportedError{TenantID: req.TenantID}
	}
	strategy := req.Strategy
	if strategy == "" {
		strategy = seal.StrategyPassphrase
	}
	if err := s.CheckBarrierMode(ctx, req.TenantID, strategy); err != nil {
		return nil, err
	}
	if _, err := s.GetTenant(ctx, req.TenantID); err != nil {
		return nil, err
	}

	out := &TenantBarrierInitBeginResult{TenantID: req.TenantID, Strategy: string(strategy)}
	var prepared *seal.ShamirInitParams
	if strategy == seal.StrategyShamir {
		split, err := s.TenantBarrierInitWithStrategy(ctx, req.TenantID, nil, req.Threshold, req.Shares, strategy, req.Roster)
		if err != nil {
			return nil, err
		}
		prepared = &seal.ShamirInitParams{Threshold: split.Threshold, Roster: split.Roster, UnsealKeyCheck: split.UnsealKeyCheck}
		out.Threshold, out.TotalShares, out.ShareCount, out.Holders = split.Threshold, split.TotalShares, split.ShareCount, split.Holders
	}

	now := s.signedInits.clock()
	statement, message, err := s.initBook.PrepareTenantInit(req.TenantID, strategy, req.KeyRef, prepared, req.SignerPublicKey, now)
	if err != nil {
		return nil, err
	}
	e := &signedInit{
		tenantID:  req.TenantID,
		strategy:  strategy,
		keyRef:    req.KeyRef,
		threshold: req.Threshold,
		shares:    req.Shares,
		prepared:  prepared,
		statement: statement,
		expiresAt: now.Add(SignedInitTTL),
	}
	ref, err := s.signedInits.issue(e)
	if err != nil {
		return nil, err
	}
	out.Reference, out.Statement, out.ExpiresAt = ref, message, e.expiresAt
	s.logger.Info("tenant barrier signed init begun",
		slog.String("tenant_id", req.TenantID),
		slog.String("strategy", string(strategy)))
	return out, nil
}

// TenantBarrierInitComplete redeems req.Reference with the administrator's
// detached signature over the statement TenantBarrierInitBegin returned and
// initializes the barrier under it, exactly as TenantBarrierInitWithStrategy
// does with that statement: the statement is claimed against the barrier's
// anchors, so only the tenant's pending first admin's key admits it.
//
// The reference is spent by the attempt whatever its outcome. One that was
// never begun, was begun for another tenant or was already redeemed is
// refused with *SignedInitError, as is one past SignedInitTTL; a signature
// that does not verify is refused with the book's *InitStatementError.
func (s *TenantService) TenantBarrierInitComplete(ctx context.Context, req *TenantBarrierInitCompleteRequest) (*TenantBarrierInitResult, error) {
	if req == nil || req.TenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if len(req.Reference) == 0 {
		return nil, &SignedInitFieldRequiredError{Field: "statement reference"}
	}
	if len(req.Signature) == 0 {
		return nil, &SignedInitFieldRequiredError{Field: "signature"}
	}
	if err := enforceTenantScope(ctx, req.TenantID, "initialize"); err != nil {
		return nil, err
	}
	if s.initBook == nil {
		return nil, &SignedInitUnsupportedError{TenantID: req.TenantID}
	}
	e, err := s.signedInits.redeem(req.TenantID, req.Reference)
	if err != nil {
		return nil, err
	}
	statement, err := s.initBook.AttachTenantInit(e.statement, req.Signature)
	if err != nil {
		return nil, err
	}
	opts := []TenantBarrierInitOption{WithInitStatement(statement)}
	if e.keyRef != "" {
		opts = append(opts, WithBarrierKeyRef(e.keyRef))
	}
	if e.prepared != nil {
		opts = append(opts, WithPreparedShamir(e.prepared.Roster, e.prepared.UnsealKeyCheck))
	}
	if req.Passphrase == nil {
		return s.TenantBarrierInitWithStrategy(ctx, req.TenantID, nil, e.threshold, e.shares, e.strategy, nil, opts...)
	}
	var result *TenantBarrierInitResult
	err = req.Passphrase.WithBytes(func(passphrase []byte) error {
		var initErr error
		result, initErr = s.TenantBarrierInitWithStrategy(ctx, req.TenantID, passphrase, e.threshold, e.shares, e.strategy, nil, opts...)
		return initErr
	})
	return result, err
}
