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

// Package secret is the config-time secret-REFERENCE resolution SPI for the
// platform stack. It is NOT a secret store — it persists nothing. Its job is to
// turn references found in configuration (e.g. `password: "credential://ca-key"`)
// into plaintext values at boot, so binaries never embed secrets and never
// hand-roll their own secret-injection logic.
//
// # Store vs. resolver
//
// The actual storage of named, versioned, rotatable credentials lives in the
// credential store (go-qrdb pkg/service CredentialService). This package sits
// ON TOP of that store: the built-in `credential://` resolver fetches values
// FROM the credential store via a CredentialRetrieveFunc. Other schemes resolve
// from a literal, a KMS, or by generating a value. So "credential" = storage;
// "secret" = the reference-resolution framework that can pull from it.
//
// # The three pieces
//
//   - SecretResolver — the plug-in interface. A resolver claims a URI scheme
//     (CanResolve / Scheme) and turns a raw reference into a SecretValue
//     (Resolve). SecretValue holds the resolved plaintext plus a ResolvedStrategy
//     tag; callers must Clear() it when done.
//   - ResolverRegistry — ordered dispatch. Register resolvers; Resolve consults
//     them in registration order and uses the first whose CanResolve matches.
//     The fallback is StrategyLiteral (the value is used verbatim).
//   - ConfigProcessor (walker.go) — reflects over a config struct, finds string
//     fields holding `scheme://ref` references, resolves each through the
//     registry, and writes the plaintext back in place. WithFailClosed() makes
//     Process return ErrUnresolvedSecretURI if any reference could not be
//     resolved — the secure default for production boots.
//
// # Scheme ownership across the stack
//
// This package defines the contract; each layer registers the resolvers for the
// schemes it owns. The contract is consumed downstream only through the SDK
// re-exports in go-qrdb/sdk/go/types_secret.go (ResolverRegistry,
// NewResolverRegistry, NewConfigProcessor, NewCredentialResolver, ...), never by
// importing this package directly.
//
//	scheme            owner         resolves to
//	----------------  ------------  -------------------------------------------
//	credential://     go-qrdb       a value from the credential store
//	literal://        go-qrdb       the verbatim value (also the no-scheme default)
//	kms://            go-quicbaas   a value sealed by a local KMS strategy
//	remote-kms://     go-quicbaas   a value from a remote KMS
//	autogen://        go-quicbaas   a freshly generated value (persisted on first use)
//	encrypted://      go-trusted-platform  a value decrypted via the platform barrier
//	idevid://         go-trusted-platform  a value bound to the device IDevID
//
// New schemes are added by implementing SecretResolver in the owning layer and
// registering it into the registry the layer builds at startup — no change to
// this package is required.
package secret
