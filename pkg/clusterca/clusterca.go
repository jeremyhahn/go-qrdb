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

// Package clusterca is the production home of the cluster CA (Key A): a
// cakey.Provider-backed signing authority for node-leaf issuance and
// member-side cluster-join CSR signing, replicated and barrier-encrypted
// through kvstore.KVStore instead of a local ca-key.pem file.
//
// It is the orchestration layer over pkg/cakey (the provider seam) and
// pkg/clustertls (the certificate-shape primitives): this package owns
// WHERE Key A lives and WHO can sign with it; pkg/clustertls owns WHAT a
// cluster certificate looks like.
package clusterca

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Prefix is the kvstore.KVStore namespace the built-in SoftwareProvider
// persists the cluster CA's wrapped key and certificate under. It carries
// no tenant: Key A is one per cluster, not one per tenant (contrast
// pkg/cert's __sys:cert_ca/{tenantID}/).
const Prefix = "__sys:cluster_ca/"

// Subject is the Subject every auto-generated cluster CA certificate
// carries, matching clustertls.GenerateCA's today for continuity.
func Subject() pkix.Name {
	return pkix.Name{
		Organization: []string{"go-quicraft"},
		CommonName:   "qrdb-ca",
	}
}

// NewProvider returns the built-in software-backed Provider for Key A:
// barrier-encrypted, persisted through kvStore. kvStore is the node's
// ordinary storage abstraction -- the local store in standalone mode, the
// replicated store in Raft mode -- so this provider behaves identically in
// either, and material adopted or generated in one mode reads back
// correctly in the other.
func NewProvider(kvStore kvstore.KVStore, barrier seal.EncryptionBarrier) *cakey.SoftwareProvider {
	return cakey.NewSoftwareProvider(kvStore, barrier, Prefix, Subject(), clustertls.CAValidity)
}

// IssueNodeLeaf generates a new ECDSA P-256 node key pair and issues a leaf
// certificate for it signed through provider, using the same template
// clustertls.GenerateNodeCert applies (SANs from clustertls.LocalSANs,
// DigitalSignature+KeyEncipherment key usage, ServerAuth+ClientAuth
// extended key usage). nodeURI is an optional SPIFFE node URI SAN.
func IssueNodeLeaf(ctx context.Context, provider cakey.Provider, nodeURI ...string) (certPEM, keyPEM []byte, err error) {
	caCert, caKey, err := material(ctx, provider)
	if err != nil {
		return nil, nil, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, &clustertls.GenerateError{Err: err}
	}

	leafTemplate, err := clustertls.NodeLeafTemplate(clustertls.NodeValidity, nodeURI...)
	if err != nil {
		return nil, nil, err
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, &clustertls.GenerateError{Err: err}
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, nil, &clustertls.GenerateError{Err: err}
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})
	return certPEM, keyPEM, nil
}

// IssueLeafForKey signs a node leaf certificate for a caller-supplied public
// key (e.g. a hardware-resident key, or a joining node's own CSR-derived
// key during member-side cluster join) using provider -- the cluster CA's
// private key is never read out of provider, only used to sign. The leaf
// carries the same template as GenerateNodeCert: CN "qrdb-node", SANs from
// clustertls.LocalSANs, KeyUsage DigitalSignature|KeyEncipherment,
// ExtKeyUsage ServerAuth+ClientAuth, clustertls.NodeValidity lifetime.
// nodeURI is an optional SPIFFE node URI SAN; a non-empty URI that fails to
// parse returns a *clustertls.GenerateError.
func IssueLeafForKey(ctx context.Context, provider cakey.Provider, pub crypto.PublicKey, nodeURI ...string) (certPEM []byte, err error) {
	caCert, caKey, err := material(ctx, provider)
	if err != nil {
		return nil, err
	}

	leafTemplate, err := clustertls.NodeLeafTemplate(clustertls.NodeValidity, nodeURI...)
	if err != nil {
		return nil, err
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, pub, caKey)
	if err != nil {
		return nil, &clustertls.GenerateError{Err: err}
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER}), nil
}

// CACertPEM returns provider's CA certificate, PEM-encoded. It is public
// data and is served even when other operations on provider would refuse
// (e.g. a barrier-sealed SoftwareProvider still answers Certificate).
func CACertPEM(ctx context.Context, provider cakey.Provider) ([]byte, error) {
	if provider == nil {
		return nil, &NilProviderError{}
	}
	cert, err := provider.Certificate(ctx)
	if err != nil {
		return nil, &ProviderError{Op: "certificate", Err: err}
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

// material resolves provider's certificate and signer together, wrapping
// either failure in a *ProviderError naming which call failed.
func material(ctx context.Context, provider cakey.Provider) (*x509.Certificate, crypto.Signer, error) {
	if provider == nil {
		return nil, nil, &NilProviderError{}
	}
	cert, err := provider.Certificate(ctx)
	if err != nil {
		return nil, nil, &ProviderError{Op: "certificate", Err: err}
	}
	signer, err := provider.Signer(ctx)
	if err != nil {
		return nil, nil, &ProviderError{Op: "signer", Err: err}
	}
	return cert, signer, nil
}
