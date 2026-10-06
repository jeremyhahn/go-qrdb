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

package cert

import (
	"context"
	"encoding/json"
	"math/big"
	"time"
)

// Revocation state in go-qrdb
//
// go-qrdb's CA is a BOOTSTRAP CA: it mints cluster-enrollment certificates
// before a cluster exists, so it cannot depend on the PKI it bootstraps.
// Every consumer of a certificate it issues is inside the cluster's own trust
// domain. There is therefore no CRL and no OCSP responder here, by design —
// revocation is enforced by reading REPLICATED STATE, which for a cluster
// member is a local read rather than a network round trip to an issuer that
// might be down. See docs/certificate-authority/architecture.md for the full
// rationale, and for why go-qrdb's CA and go-xkms's product CA are two CAs
// on purpose.
//
// The authoritative revocation state is the MARKER under
// "__sys:certs_revoked/{serial}". CertRecord.Revoked is a display field: it is
// what an operator sees in a listing, it is best-effort (a record this node
// does not hold, or cannot decode, is simply not updated), and nothing
// consults it to make an access-control decision.
//
// Revocation is a ONE-WAY DOOR. Nothing in this package deletes a marker for a
// certificate that is still inside its validity window, and no exported
// operation — on any transport — un-revokes. The only removal is
// ReapExpiredRevocations below, which drops markers whose certificate has
// already expired and would fail validation regardless. That is what a signed,
// monotonic CRL gives for free and what a KV row does not: without it, DELETE
// access on the row would be the power to silently un-revoke.

// revocationEntry is the value stored under a "__sys:certs_revoked/{serial}"
// marker.
//
// The marker's PRESENCE is what revokes; the payload only carries what is
// needed to reap it later. IsRevoked therefore never parses it — a marker
// written by an older build (the literal "1"), or one whose payload is
// corrupt, still revokes. Fail-closed is the only acceptable reading of a
// revocation marker.
type revocationEntry struct {
	// Serial is the revoked certificate serial, in the same lowercase
	// base-16 form FormatSerial produces.
	Serial string `json:"serial"`

	// RevokedAt is when the marker was written.
	RevokedAt time.Time `json:"revoked_at"`

	// NotAfter is the revoked certificate's expiry, copied from this node's
	// certificate record at revocation time.
	//
	// It is ZERO when this node held no record for the serial — the
	// deny-by-serial case, revoking a certificate issued by a peer or one
	// whose record was pruned. Such a marker has no known expiry, so it is
	// never reaped and is permanent by construction. That is the accepted
	// cost of keeping deny-by-serial: a serial nobody here can date cannot be
	// proven safe to forget.
	NotAfter time.Time `json:"not_after,omitempty"`
}

// FormatSerial renders a certificate serial number in the canonical form
// go-qrdb stores and compares it by: lowercase base-16, unpadded.
//
// It is the SINGLE definition of that format. IssueClientCert keys its
// certificate records with it, RevokeCert keys the revocation marker with it,
// and RevocationCache derives the lookup key for a peer certificate with it.
// Two of those three deriving the format independently is how an enforcement
// check silently matches nothing.
func FormatSerial(serial *big.Int) string {
	if serial == nil {
		return ""
	}
	return serial.Text(16)
}

// ReapExpiredRevocations deletes revocation markers whose certificate has
// already expired, and returns the number of markers removed.
//
// A revoked serial past its notAfter can be dropped because an expired
// certificate fails validation on its own: removing the marker cannot restore
// access. Without this the namespace only ever grows, and it grows fastest in
// exactly the deployment that revokes most.
//
// Markers with no recorded expiry — the deny-by-serial case, and markers
// written before the payload carried one — are NEVER reaped. This method is
// the only code path in go-qrdb that removes a revocation marker, and its
// predicate is "the certificate is already expired", never anything about the
// caller.
//
// RevokeCert calls it on every revocation, so the namespace is bounded without
// a background sweeper. It is exported so an embedder can also run it
// directly.
func (p *DefaultCertProvider) ReapExpiredRevocations(ctx context.Context) (int, error) {
	if p.barrier.IsSealed() {
		return 0, &BarrierSealedError{}
	}

	now := time.Now().UTC()
	var expired []string
	if err := p.kvStore.Scan(ctx, prefixCertsRevoked, func(key string, value []byte) error {
		var entry revocationEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			// Unparseable marker: it still revokes, and it carries no expiry
			// to justify dropping it. Leave it.
			return nil
		}
		if entry.NotAfter.IsZero() || !entry.NotAfter.Before(now) {
			return nil
		}
		expired = append(expired, key)
		return nil
	}); err != nil {
		return 0, &StoreError{Op: "scan", Detail: prefixCertsRevoked, Err: err}
	}

	reaped := 0
	for _, key := range expired {
		if err := p.kvStore.Delete(ctx, key); err != nil {
			return reaped, &StoreError{Op: "delete", Detail: key, Err: err}
		}
		reaped++
	}
	return reaped, nil
}
