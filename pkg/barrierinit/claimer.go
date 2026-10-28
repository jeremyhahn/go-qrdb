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

package barrierinit

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// AnchorSource returns what b's statements verify against.
type AnchorSource func(ctx context.Context, b clusterseal.Barrier) (Anchors, error)

// claimBook is the part of Book a Claimer writes and reads through.
type claimBook interface {
	Claim(ctx context.Context, s InitStatement, a Anchors) error
	Versions(ctx context.Context, barrier clusterseal.Barrier) ([]InitStatement, error)
}

var _ claimBook = (*Book)(nil)

// Claimer claims a tenant barrier's epoch-1 statement as it arrives with the
// owner's init request: encoded, beside the strategy and key reference the
// request initializes this node's key with.
type Claimer struct {
	book    claimBook
	anchors AnchorSource
}

// NewClaimer returns a Claimer writing through book and verifying against
// anchors.
func NewClaimer(book *Book, anchors AnchorSource) *Claimer {
	return &Claimer{book: book, anchors: anchors}
}

// ClaimTenantInit parses raw and claims it as tenantID's epoch-1 statement,
// once it names exactly that barrier, strategyID and keyRef: a statement for
// any other init is refused with ReasonMismatch before it is verified or
// stored.
func (c *Claimer) ClaimTenantInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string, raw []byte) error {
	return c.claimTenant(ctx, Expect{Barrier: clusterseal.TenantBarrier(tenantID), Epoch: 1, StrategyID: strategyID, KeyRef: keyRef}, raw)
}

// ClaimTenantShamirInit is ClaimTenantInit for the second request of a Shamir
// init: the statement must sign exactly the split this node prepared --
// prepared's threshold, roster and unseal key check value -- or it is refused
// with ReasonMismatch before anything is written.
func (c *Claimer) ClaimTenantShamirInit(ctx context.Context, tenantID string, prepared *seal.ShamirInitParams, raw []byte) error {
	if prepared == nil {
		return &InitStatementError{Barrier: clusterseal.TenantBarrier(tenantID), Epoch: 1, Reason: ReasonMismatch}
	}
	return c.claimTenant(ctx, Expect{
		Barrier: clusterseal.TenantBarrier(tenantID), Epoch: 1, StrategyID: seal.StrategyShamir,
		Threshold: prepared.Threshold, Roster: prepared.Roster, UnsealKeyCheck: prepared.UnsealKeyCheck,
	}, raw)
}

// PrepareTenantInit returns tenantID's unsigned epoch-1 statement naming the
// signing key signerSPKI (a DER SubjectPublicKeyInfo), encoded, and the
// message that key's holder signs: the first half of a two-phase init, whose
// signature is made away from this node. prepared is the Shamir split this
// node prepared, nil for every other strategy, which keyRef then names the
// key of. An unusable key or ill-formed statement is refused with
// *InitStatementError.
func (c *Claimer) PrepareTenantInit(tenantID string, strategyID seal.StrategyID, keyRef string, prepared *seal.ShamirInitParams, signerSPKI []byte, issuedAt time.Time) (statement, message []byte, err error) {
	s := NewTenantInit(tenantID, string(strategyID), keyRef, issuedAt)
	if prepared != nil {
		s = NewTenantShamirInit(tenantID, prepared.Threshold, prepared.Roster, prepared.UnsealKeyCheck, issuedAt)
	}
	pub, err := x509.ParsePKIXPublicKey(signerSPKI)
	if err != nil {
		return nil, nil, s.refuse(ReasonMalformedKey, err)
	}
	if s, err = ForSigner(s, pub); err != nil {
		return nil, nil, err
	}
	if statement, err = s.Marshal(); err != nil {
		return nil, nil, s.refuse(ReasonMalformed, err)
	}
	return statement, s.Message(), nil
}

// AttachTenantInit returns the encoded statement PrepareTenantInit returned,
// carrying signature, once signature verifies over its message under the key
// it names; one that does not is refused with *InitStatementError naming
// ReasonBadSignature. Whether that key may sign for the barrier is decided
// when the statement is claimed, against the barrier's anchors.
func (c *Claimer) AttachTenantInit(statement, signature []byte) ([]byte, error) {
	s, err := Parse(statement)
	if err != nil {
		return nil, err
	}
	if s, err = Attach(s, signature); err != nil {
		return nil, err
	}
	raw, err := s.Marshal()
	if err != nil {
		return nil, s.refuse(ReasonMalformed, err)
	}
	return raw, nil
}

// claimTenant claims raw for the tenant barrier e names once it matches e.
func (c *Claimer) claimTenant(ctx context.Context, e Expect, raw []byte) error {
	s, err := Parse(raw)
	if err != nil {
		return err
	}
	b := e.Barrier
	if err := s.Matches(e); err != nil {
		return err
	}
	a, err := c.anchors(ctx, b)
	if err != nil {
		return &StoreError{Op: "read anchors", Err: err}
	}
	return c.book.Claim(ctx, s, a)
}

// TenantInit returns tenantID's current init statement -- its latest
// recorded epoch -- once it verifies against the barrier's anchors. It is
// what a node reattaching an initialized tenant barrier unseals from: the
// strategy, key reference and epoch the barrier's owner signed, never a copy
// of them held anywhere unsigned. A barrier with no record is refused with
// *NoInitStatementError; one whose statement no longer verifies, with the
// *InitStatementError Verify returns.
func (c *Claimer) TenantInit(ctx context.Context, tenantID string) (InitStatement, error) {
	b := clusterseal.TenantBarrier(tenantID)
	versions, err := c.book.Versions(ctx, b)
	if err != nil {
		return InitStatement{}, err
	}
	if len(versions) == 0 {
		return InitStatement{}, &NoInitStatementError{Barrier: b}
	}
	latest := versions[len(versions)-1]
	a, err := c.anchors(ctx, b)
	if err != nil {
		return InitStatement{}, &StoreError{Op: "read anchors", Err: err}
	}
	if err := Verify(latest, a); err != nil {
		return InitStatement{}, err
	}
	return latest, nil
}
