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

package clusterenroll

import (
	"context"
	"encoding/pem"
	"errors"
	"log/slog"
	"strings"
)

// AnchorError is returned when an anchor-only bundle fails validation — the
// fetched or advertised material must contain at least one certificate and no
// private-key material.
type AnchorError struct {
	// Reason describes the validation failure.
	Reason string
}

// Error returns a human-readable description.
func (e *AnchorError) Error() string {
	return "clusterenroll: invalid trust-anchor bundle: " + e.Reason
}

// anchorBundleProvider implements noiseboot.BundleProvider by serving only
// the trust-anchor certificate chain — never a private key. It is the
// advertise-side counterpart of FetchAnchor, used by attestation-based
// enrollment modes where joining nodes obtain their node certificate from a
// certificate authority (e.g. ACME enroll-01) instead of self-issuing from a
// shared CA key.
type anchorBundleProvider struct {
	anchorPEM []byte
}

// CABundle returns the trust-anchor certificate chain as PEM bytes.
func (p *anchorBundleProvider) CABundle() ([]byte, error) {
	return p.anchorPEM, nil
}

// ValidateAnchorBundle verifies that anchorPEM contains at least one
// CERTIFICATE block and no private-key material. It fails closed: a bundle
// carrying any PRIVATE KEY block is rejected so an attested-mode node can
// never accidentally advertise (or accept) a CA signing key as a trust
// anchor.
func ValidateAnchorBundle(anchorPEM []byte) error {
	sawCert := false
	rest := anchorPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return &AnchorError{Reason: "bundle contains private-key material (" + block.Type + ")"}
		}
		if block.Type == "CERTIFICATE" {
			sawCert = true
		}
	}
	if !sawCert {
		return &AnchorError{Reason: "no CERTIFICATE block found"}
	}
	return nil
}

// StartAnchorAdvertiser starts the Noise NK bootstrap server serving an
// anchor-only bundle: the trust-anchor certificate chain WITHOUT any private
// key. Joining nodes fetch it with FetchAnchor, then obtain their own node
// certificate from the certificate authority the anchor chain belongs to
// (e.g. via ACME enroll-01 TPM attestation).
//
// The server's static Noise key is persisted to {dataDir}/noise-key.hex
// exactly as StartAdvertiser does, so a node can switch enrollment modes
// without changing its advertised Noise identity. The anchor bundle is
// validated with ValidateAnchorBundle before the server starts.
func StartAnchorAdvertiser(anchorPEM []byte, opts AdvertiseOptions, dataDir string, logger *slog.Logger) (*Advertiser, error) {
	if err := ValidateAnchorBundle(anchorPEM); err != nil {
		return nil, err
	}
	return startNoiseAdvertiser(&anchorBundleProvider{anchorPEM: anchorPEM}, opts, dataDir, logger)
}

// FetchAnchor fetches the trust-anchor bundle from an existing cluster node
// via truststrap AutoFetch (Noise / SPKI / DANE / Direct, per opts.Bootstrap)
// and validates it as anchor-only material. It is the join-side counterpart
// of StartAnchorAdvertiser.
//
// FetchAnchor fails closed with AnchorError when the fetched bundle carries
// private-key material: that indicates the advertising node is running the
// shared-CA-key enrollment mode (StartAdvertiser), which is incompatible with
// attestation-based enrollment and must be surfaced as a configuration
// mismatch rather than silently accepted.
func FetchAnchor(ctx context.Context, opts *Options, logger *slog.Logger) ([]byte, error) {
	if !opts.Bootstrap.HasFlags() {
		return nil, &FetchError{Method: "auto", Err: errors.New("no bootstrap method configured")}
	}
	bundlePEM, err := autoFetchBundle(ctx, opts, logger)
	if err != nil {
		return nil, err
	}
	if err := ValidateAnchorBundle(bundlePEM); err != nil {
		return nil, err
	}
	return bundlePEM, nil
}
