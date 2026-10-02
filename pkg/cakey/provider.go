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

// Package cakey defines the single seam through which every certificate
// authority in go-qrdb obtains its signing key and certificate: the cluster
// CA (which also backs the ceremony CA and node-leaf issuance) and the login
// CA (pkg/cert). Nothing outside this package ever holds a CA private key
// directly -- every signature is produced by calling Provider.Signer and
// using the returned crypto.Signer, never by reading key bytes out of it.
//
// go-qrdb is embedded as the datastore and consensus engine under go-xkms
// and other projects that bring their own key custody (software, TPM 2.0,
// PKCS#11, cloud KMS). Provider is shaped so that an adapter over go-xkms's
// pkg/ca.CASigningIdentity (Signer) and pkg/ca.CACertificateAccessor
// (Certificate) satisfies it with nothing more than Adapter -- go-qrdb never
// imports go-xkms, so the fit is verified here with a same-shaped test
// double, not with the real type.
package cakey

import (
	"context"
	"crypto"
	"crypto/x509"
)

// Provider is the minimal seam a CA needs to sign something: a
// crypto.Signer backed by the CA's private key, and the CA's own
// certificate. It never exposes the private key itself.
//
// Thread-safe: implementations must support concurrent calls from every
// signing path (client-cert issuance, ceremony CSR signing, node-leaf
// issuance, member-side cluster-join CSR signing).
type Provider interface {
	// Signer returns a crypto.Signer backed by the CA's private key. With a
	// hardware or remote backend (HSM, TPM, KMS) the returned Signer routes
	// Sign calls to that device; the key material itself never enters this
	// process. Returns *BarrierSealedError when the key is barrier-protected
	// and the barrier is currently sealed, and *NotInitializedError when no
	// key has ever been established.
	Signer(ctx context.Context) (crypto.Signer, error)

	// Certificate returns the CA's own certificate. Unlike Signer, this is
	// public data and implementations are encouraged to serve it even while
	// otherwise sealed, mirroring cert.CertProvider.CACertPEM's contract.
	Certificate(ctx context.Context) (*x509.Certificate, error)
}

// Adapter composes two independently-sourced functions into a Provider. It
// is the "no glue" seam an embedder's own CA type is wired through: an
// xkms-backed CA implements pkg/ca.CASigningIdentity.Signer and
// pkg/ca.CACertificateAccessor.Certificate (closed over its own
// types.KeyAttributes), and those two methods alone are enough to build an
// Adapter without copying any xkms type into go-qrdb.
type Adapter struct {
	// SignerFunc produces the signer. Required.
	SignerFunc func(ctx context.Context) (crypto.Signer, error)

	// CertificateFunc produces the certificate. Required.
	CertificateFunc func(ctx context.Context) (*x509.Certificate, error)
}

// Compile-time interface check.
var _ Provider = Adapter{}

// Signer calls SignerFunc.
func (a Adapter) Signer(ctx context.Context) (crypto.Signer, error) {
	return a.SignerFunc(ctx)
}

// Certificate calls CertificateFunc.
func (a Adapter) Certificate(ctx context.Context) (*x509.Certificate, error) {
	return a.CertificateFunc(ctx)
}
