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

package ceremony

import (
	"crypto"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// ShareClaimContext is the domain-separation prefix of the message a holder
// signs to claim a share envelope.
//
// The claim proof and the mtls quorum factor's answer are both signatures by
// the holder's certificate key over a server nonce. Signed over the bare
// nonce, a signature obtained for one could be presented for the other. Each
// therefore signs a domain-separated message of its own: the claim this
// context, the scope's claim label, the holder and the nonce; the mtls factor
// mtls.ChallengeMessage (its own context, the challenge ID, the quorum
// purpose and the nonce). Neither signature verifies as the other.
const ShareClaimContext = "qrdb/share-claim/v1"

// ShareClaimMessage returns the message a holder signs to claim a share
// envelope: ShareClaimContext, the scope's claim label, the username and the
// nonce, NUL-separated. Neither a label nor a holder name can contain NUL, so
// distinct claims never share a message.
func ShareClaimMessage(scopeLabel, username string, nonce []byte) []byte {
	msg := make([]byte, 0, len(ShareClaimContext)+len(scopeLabel)+len(username)+len(nonce)+3)
	msg = append(msg, ShareClaimContext...)
	msg = append(msg, 0)
	msg = append(msg, scopeLabel...)
	msg = append(msg, 0)
	msg = append(msg, username...)
	msg = append(msg, 0)
	return append(msg, nonce...)
}

// SignShareClaim signs the claim of username's envelope in the scope labelled
// scopeLabel over nonce, with the holder's certificate key, in the signature
// format of mtls.SignNonce.
func SignShareClaim(signer crypto.Signer, scopeLabel, username string, nonce []byte) ([]byte, error) {
	if len(nonce) == 0 {
		return nil, &mtls.UnsupportedSignerError{}
	}
	return mtls.SignNonce(signer, ShareClaimMessage(scopeLabel, username, nonce))
}

// verifyShareClaim reports whether signature is pub's claim signature for
// username in the scope labelled scopeLabel over nonce.
func verifyShareClaim(pub crypto.PublicKey, scopeLabel, username string, nonce, signature []byte) bool {
	if len(nonce) == 0 {
		return false
	}
	return mtls.VerifyNonceSignature(pub, ShareClaimMessage(scopeLabel, username, nonce), signature)
}

// ClaimLabel returns the scope's label in a share-claim message, as the
// holder names the barrier: "system", "recovery", "tenant:<id>", or
// "database:<name>". A database is named without its tenant, which the
// holder's own identity fixes; the nonce, issued for exactly this scope and
// holder, binds the rest.
func (b BarrierScope) ClaimLabel() string {
	switch b.kind {
	case scopeTenant:
		return TenantClaimLabel(b.tenantID)
	case scopeDatabase:
		return DatabaseClaimLabel(b.database)
	case scopeRecovery:
		return RecoveryClaimLabel
	default:
		return SystemClaimLabel
	}
}

// Claim labels of the system barrier's two domains.
const (
	// SystemClaimLabel labels a claim in the system barrier's unseal domain.
	SystemClaimLabel = "system"
	// RecoveryClaimLabel labels a claim in the system barrier's recovery
	// domain.
	RecoveryClaimLabel = "recovery"
)

// TenantClaimLabel returns the claim label of tenantID's barrier.
func TenantClaimLabel(tenantID string) string {
	return "tenant:" + tenantID
}

// DatabaseClaimLabel returns the claim label of database's barrier.
func DatabaseClaimLabel(database string) string {
	return "database:" + database
}
