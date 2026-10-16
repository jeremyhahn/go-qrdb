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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/cakey"

// ---------------------------------------------------------------------------
// CA signing-key seam
//
// Every certificate authority in go-qrdb -- the cluster CA (Key A) and each
// tenant's login CA (Key B) -- signs through a CAKeyProvider and never holds
// the private key itself. An embedder that keeps its keys in hardware or a
// remote KMS implements these types and passes them to
// node.WithClusterCAKeyProvider (Key A) and node.WithCertKeyProviderFactory
// (Key B). (Aliases pkg/cakey.)
// ---------------------------------------------------------------------------

// CAKeyProvider is the seam a CA signs through: a crypto.Signer backed by the
// CA's private key, and the CA's own certificate. (Aliases
// pkg/cakey.Provider.)
type CAKeyProvider = cakey.Provider

// CAKeyAdapter builds a CAKeyProvider from two functions, so an embedder's own
// CA type needs no wrapper type of its own. Both functions are required.
// (Aliases pkg/cakey.Adapter.)
type CAKeyAdapter = cakey.Adapter

// CAKeyCertOnlyProvider is the capability a CAKeyProvider may additionally
// offer: its certificate resolved without ever creating a CA as a side effect.
// (Aliases pkg/cakey.CertOnlyProvider.)
type CAKeyCertOnlyProvider = cakey.CertOnlyProvider

// CAKeyProviderFactory resolves the CAKeyProvider that signs one tenant's
// Key B (login CA) material, one key per tenant, and names every tenant it
// backs. A single provider shared across tenants would make the issuer-tenant
// binding vacuous, which is why no adapter lifts one into a factory.
// (Aliases pkg/cakey.ProviderFactory.)
type CAKeyProviderFactory = cakey.ProviderFactory

// CAKeyStaticProvider is a CAKeyProvider over a signer and certificate the
// caller already holds. (Aliases pkg/cakey.StaticProvider.)
type CAKeyStaticProvider = cakey.StaticProvider

// NewCAKeyStaticProvider builds a CAKeyStaticProvider; a nil signer or
// certificate is refused. (Aliases pkg/cakey.NewStaticProvider.)
var NewCAKeyStaticProvider = cakey.NewStaticProvider

// ---------------------------------------------------------------------------
// CA signing-key errors a provider returns and a caller inspects
// ---------------------------------------------------------------------------

// CAKeyBarrierSealedError says the key is barrier-protected and the barrier
// is sealed. (Aliases pkg/cakey.BarrierSealedError.)
type CAKeyBarrierSealedError = cakey.BarrierSealedError

// CAKeyNotInitializedError says no CA key has ever been established.
// (Aliases pkg/cakey.NotInitializedError.)
type CAKeyNotInitializedError = cakey.NotInitializedError

// CAKeySignerRevokedError says the signer was revoked after it was obtained.
// (Aliases pkg/cakey.SignerRevokedError.)
type CAKeySignerRevokedError = cakey.SignerRevokedError

// CAKeyNilProviderError says a nil provider was supplied where one is
// required; a nil seam is refused rather than replaced by a built-in key.
// (Aliases pkg/cakey.NilProviderError.)
type CAKeyNilProviderError = cakey.NilProviderError
