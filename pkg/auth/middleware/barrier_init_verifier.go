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

package middleware

import (
	"crypto/tls"
	"crypto/x509"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TenantBarrierInitKey returns the SPKI thumbprint (user.ComputeSPKIThumbprint)
// of the key a tenant barrier init proof must be by. Empty means there is no
// key the proof could be by, and the proof is refused
// initproof.ReasonNoPendingAdmin; an error refuses it with that error. It is
// called only after the nonce is redeemed, so a lookup failure still spends
// the nonce.
type TenantBarrierInitKey func() (string, error)

// TenantBarrierInitVerifier is the server side of signed tenant barrier
// initialization: init/begin issues a single-use nonce bound to the tenant
// and the connection's TLS exporter (RFC 9266), and init/complete checks the
// proof against that nonce, that exporter and the expected key, and consumes
// the nonce. It is the one implementation every qrdb transport runs through
// AuthMiddleware, and the one a remote facade terminating the caller's TLS
// runs itself. It decides only the proof; who may begin or complete an init
// is the caller's authorization (AuthMiddleware.AuthorizeTenantBarrierInit).
// It is safe for concurrent use.
type TenantBarrierInitVerifier struct {
	book *initproof.Book
}

// NewTenantBarrierInitVerifier returns a verifier over book, the nonces it
// issues and redeems. A nil book is a fresh one on the real clock with
// initproof.DefaultMaxOutstanding.
func NewTenantBarrierInitVerifier(book *initproof.Book) *TenantBarrierInitVerifier {
	if book == nil {
		book = initproof.NewBook(nil, 0)
	}
	return &TenantBarrierInitVerifier{book: book}
}

// Begin issues the init nonce for tenantID bound to cs's TLS exporter. A
// connection with no TLS 1.3 exporter is refused with
// *tenant.FirstAdminProofError (initproof.ReasonNoExporter): no complete could
// ever succeed on it. A verifier holding its book's limit of unexpired
// challenges refuses with *initproof.TooManyChallengesError until some lapse.
func (v *TenantBarrierInitVerifier) Begin(tenantID string, cs *tls.ConnectionState) (*initproof.Challenge, error) {
	exporter, err := initproof.Exporter(cs)
	if err != nil {
		return nil, &tenant.FirstAdminProofError{TenantID: tenantID, Reason: initproof.ReasonNoExporter, Err: err}
	}
	return v.book.Issue(tenantID, exporter)
}

// Complete checks proof for tenantID on the connection cs: the nonce must be
// one Begin issued for tenantID on this connection and not yet redeemed or
// expired, the key must be the one key names, and the signature must be that
// key's over the tenant, the nonce and cs's exporter (initproof.Verify). The
// nonce is redeemed before the key and signature are examined, so a wrong-key
// attempt spends it exactly as a right one does.
//
// Every refusal of the proof itself is *tenant.FirstAdminProofError naming
// its initproof.Reason; an error from key is returned as it is.
func (v *TenantBarrierInitVerifier) Complete(tenantID string, cs *tls.ConnectionState, proof *TenantBarrierInitProof, key TenantBarrierInitKey) error {
	refuse := func(reason initproof.Reason, err error) error {
		return &tenant.FirstAdminProofError{TenantID: tenantID, Reason: reason, Err: err}
	}
	if proof == nil || len(proof.Nonce) == 0 || len(proof.PublicKey) == 0 || len(proof.Signature) == 0 {
		return refuse(initproof.ReasonAbsent, nil)
	}
	exporter, err := initproof.Exporter(cs)
	if err != nil {
		return refuse(initproof.ReasonNoExporter, err)
	}
	if err := v.book.Consume(tenantID, proof.Nonce, exporter); err != nil {
		var ce *initproof.ChallengeError
		if errors.As(err, &ce) {
			return refuse(ce.Reason, err)
		}
		return refuse(initproof.ReasonUnknownNonce, err)
	}
	pub, err := x509.ParsePKIXPublicKey(proof.PublicKey)
	if err != nil {
		return refuse(initproof.ReasonMalformedKey, err)
	}
	if key == nil {
		return refuse(initproof.ReasonNoPendingAdmin, nil)
	}
	want, err := key()
	if err != nil {
		return err
	}
	if want == "" {
		return refuse(initproof.ReasonNoPendingAdmin, nil)
	}
	got, err := user.PublicKeySPKIThumbprint(pub)
	if err != nil || got != want {
		return refuse(initproof.ReasonWrongKey, err)
	}
	if !initproof.Verify(pub, tenantID, proof.Nonce, exporter, proof.Signature) {
		return refuse(initproof.ReasonBadSignature, nil)
	}
	return nil
}

// Outstanding reports how many unexpired challenges the verifier holds.
func (v *TenantBarrierInitVerifier) Outstanding() int {
	return v.book.Outstanding()
}
