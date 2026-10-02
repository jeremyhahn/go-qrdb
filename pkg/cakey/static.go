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

package cakey

import (
	"context"
	"crypto"
	"crypto/x509"
)

// StaticProvider wraps a signer and certificate that already exist in
// memory and serves them unconditionally -- there is no barrier, no store,
// and no unseal lifecycle.
//
// It exists for exactly one production use: the cluster CA (Key A) during
// the window between its generation on the first node and the first
// successful write of its wrapped key into the replicated store (see
// docs/certificate-authority/architecture.md, "Bootstrap crash window").
// The initial M-of-N ceremony signs its first officer CSRs against the
// freshly generated key while it is still in memory and the barrier does
// not exist yet to protect anything; StaticProvider is what lets that
// signing go through the same Provider seam as every later, store-backed
// signature rather than a separate code path. It is also the natural test
// double for anything that only needs a Provider, not a full
// SoftwareProvider.
type StaticProvider struct {
	signer crypto.Signer
	cert   *x509.Certificate
}

// Compile-time interface check.
var _ Provider = (*StaticProvider)(nil)

// NewStaticProvider returns a Provider that always answers with signer and
// cert. Neither argument may be nil.
func NewStaticProvider(signer crypto.Signer, cert *x509.Certificate) (*StaticProvider, error) {
	if signer == nil || cert == nil {
		return nil, &GenerateError{Err: errNilStaticMaterial}
	}
	return &StaticProvider{signer: signer, cert: cert}, nil
}

// Signer returns the wrapped signer.
func (p *StaticProvider) Signer(context.Context) (crypto.Signer, error) {
	return p.signer, nil
}

// Certificate returns the wrapped certificate.
func (p *StaticProvider) Certificate(context.Context) (*x509.Certificate, error) {
	return p.cert, nil
}

// errNilStaticMaterial is the underlying error wrapped by GenerateError when
// NewStaticProvider is given a nil signer or certificate.
var errNilStaticMaterial = staticMaterialError("cakey: signer and certificate must both be non-nil")

// staticMaterialError is a trivial string-backed error type so the nil-input
// case stays a typed error (via GenerateError's Unwrap) without reaching for
// errors.New/fmt.Errorf at the call site.
type staticMaterialError string

func (e staticMaterialError) Error() string { return string(e) }
