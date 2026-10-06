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

package trustanchor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// MaxAnchorsPerTenant bounds how many anchors one tenant may register. Every
// tenant's anchors join the shared client-auth trust pool, so the bound keeps
// one tenant from inflating every handshake's pool.
const MaxAnchorsPerTenant = 32

// MaxAnchorPEMSize bounds a single anchor certificate's DER encoding.
// 16 KiB is generous for any real CA certificate, including one carrying an
// unusually large extension, while refusing the kind of oversized blob a
// client could otherwise wedge into the replicated store through a
// certificate nobody needs to read, only to store (go-qrdb security review).
const MaxAnchorPEMSize = 16 * 1024

// MaxBundlePEMSize bounds an entire PEM bundle offered to ParseAnchors or
// KVRegistry.Add, checked before anything in it is parsed. It is sized for
// MaxAnchorsPerTenant anchors at MaxAnchorPEMSize each, plus slack for PEM
// armor and the duplicates a bundle may legitimately carry.
const MaxBundlePEMSize = MaxAnchorPEMSize * (MaxAnchorsPerTenant + 8)

// disallowedAnchorSignatureAlgorithms are the signature algorithms no
// certificate may use to BE a trust anchor, even though crypto/x509 parses
// them: each is a broken or deprecated digest a forger can exploit to
// produce a colliding certificate (go-qrdb security review).
var disallowedAnchorSignatureAlgorithms = map[x509.SignatureAlgorithm]bool{
	x509.MD2WithRSA:    true,
	x509.MD5WithRSA:    true,
	x509.SHA1WithRSA:   true,
	x509.DSAWithSHA1:   true,
	x509.ECDSAWithSHA1: true,
}

// Fingerprint returns cert's anchor identifier: the lowercase hex SHA-256 of
// its DER encoding.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// ParseCertificates decodes every CERTIFICATE block in a PEM bundle, in
// order. Blocks of any other type are skipped. It does not judge whether the
// certificates are usable as anchors; ParseAnchors does.
func ParseCertificates(bundlePEM []byte) ([]*x509.Certificate, error) {
	if len(bundlePEM) > MaxBundlePEMSize {
		return nil, &BundleTooLargeError{Size: len(bundlePEM), Max: MaxBundlePEMSize}
	}
	var certs []*x509.Certificate
	rest := bundlePEM
	for i := 0; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if len(block.Bytes) > MaxAnchorPEMSize {
			return nil, &InvalidAnchorError{Index: i, Reason: "its DER encoding exceeds the per-anchor size limit"}
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, &InvalidAnchorError{Index: i, Reason: "it does not parse: " + err.Error()}
		}
		certs = append(certs, c)
		i++
	}
	if len(certs) == 0 {
		return nil, &EmptyBundleError{}
	}
	return certs, nil
}

// ParseAnchors decodes a PEM bundle and checks that every certificate in it
// can be a trust anchor at now: a CA certificate (basic constraints present,
// CA true) that may sign certificates when it declares key usage at all, and
// inside its validity window. Duplicates within the bundle are collapsed.
func ParseAnchors(bundlePEM []byte, now time.Time) ([]*x509.Certificate, error) {
	certs, err := ParseCertificates(bundlePEM)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(certs))
	out := make([]*x509.Certificate, 0, len(certs))
	for i, c := range certs {
		if !c.BasicConstraintsValid || !c.IsCA {
			return nil, &InvalidAnchorError{Index: i, Reason: "it is not a CA certificate"}
		}
		if c.KeyUsage != 0 && c.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, &InvalidAnchorError{Index: i, Reason: "its key usage does not permit certificate signing"}
		}
		if now.Before(c.NotBefore) || now.After(c.NotAfter) {
			return nil, &InvalidAnchorError{Index: i, Reason: "it is outside its validity window"}
		}
		if disallowedAnchorSignatureAlgorithms[c.SignatureAlgorithm] {
			return nil, &InvalidAnchorError{Index: i, Reason: "it is signed with " + c.SignatureAlgorithm.String() + ", which a trust anchor may not use"}
		}
		// The same key-strength floor a client certificate is issued for
		// (RSA >= 2048 bits, ECDSA P-256/384/521, or Ed25519) applies to a
		// certificate trusted to VOUCH for one: P-224 and anything weaker is
		// refused here for the identical reason cert.CheckClientKey refuses
		// it for a CSR.
		if err := cert.CheckClientKey(c.PublicKey); err != nil {
			return nil, &InvalidAnchorError{Index: i, Reason: "its key is not strong enough for a trust anchor: " + err.Error()}
		}
		fp := Fingerprint(c)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		out = append(out, c)
	}
	return out, nil
}

// EncodePEM encodes a certificate as a PEM block.
func EncodePEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// anchorOf describes a parsed certificate as an Anchor.
func anchorOf(cert *x509.Certificate, addedAt time.Time) Anchor {
	return Anchor{
		Fingerprint: Fingerprint(cert),
		Subject:     cert.Subject.String(),
		NotBefore:   cert.NotBefore,
		NotAfter:    cert.NotAfter,
		CertPEM:     EncodePEM(cert),
		AddedAt:     addedAt,
	}
}

// tenantAnchors reads and parses one tenant's anchors through src. A bundle
// the source returns that does not parse is an error: a trust decision is
// never made on a partial read.
func tenantAnchors(ctx context.Context, src Source, tenantID string) ([]*x509.Certificate, error) {
	bundle, err := src.CABundle(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(bundle)) == 0 {
		return nil, nil
	}
	return ParseCertificates(bundle)
}

// ChainContainsAnchor reports whether any certificate of a verified chain
// other than the leaf (chain[0]) is byte-identical to one of tenantID's
// anchors. It is the anchor half of the issuer-tenant binding: the chain was
// already verified against the shared trust pool, so its containing one of
// THIS tenant's anchors is exactly "an anchor of this tenant vouches for this
// certificate", however many intermediates stand between them.
func ChainContainsAnchor(ctx context.Context, src Source, tenantID string, chain []*x509.Certificate) (bool, error) {
	if src == nil || len(chain) < 2 {
		return false, nil
	}
	anchors, err := tenantAnchors(ctx, src, tenantID)
	if err != nil {
		return false, err
	}
	for _, a := range anchors {
		for _, c := range chain[1:] {
			if c != nil && bytes.Equal(c.Raw, a.Raw) {
				return true, nil
			}
		}
	}
	return false, nil
}

// VerifyClientCertificate verifies a bring-your-own client certificate
// against tenantID's anchors at now: it must chain, through the supplied
// intermediates if any, to one of them, for client authentication. It
// returns *NoAnchorsError when the tenant has none and
// *CertificateNotTrustedError when the chain does not verify.
func VerifyClientCertificate(ctx context.Context, src Source, tenantID string, leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) error {
	anchors, err := tenantAnchors(ctx, src, tenantID)
	if err != nil {
		return err
	}
	return verifyAgainst(tenantID, anchors, leaf, intermediates, now)
}

// VerifyClientCertificateAgainst is VerifyClientCertificate over an explicit
// anchor set rather than a registry. Tenant onboarding uses it to check a
// bring-your-own admin certificate against the anchors in the same request
// BEFORE anything is written.
func VerifyClientCertificateAgainst(tenantID string, anchors []*x509.Certificate, leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) error {
	return verifyAgainst(tenantID, anchors, leaf, intermediates, now)
}

func verifyAgainst(tenantID string, anchors []*x509.Certificate, leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) error {
	if len(anchors) == 0 {
		return &NoAnchorsError{TenantID: tenantID}
	}
	if leaf == nil {
		return &CertificateNotTrustedError{TenantID: tenantID}
	}
	roots := x509.NewCertPool()
	for _, a := range anchors {
		roots.AddCert(a)
	}
	inter := x509.NewCertPool()
	for _, c := range intermediates {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return &CertificateNotTrustedError{TenantID: tenantID, Err: err}
	}
	return nil
}
