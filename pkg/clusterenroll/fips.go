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

import "crypto/fips140"

// noiseAvailable reports whether the Noise NK enrollment method may be used in
// this process.
//
// Noise_NK_25519_ChaChaPoly_SHA256 mandates X25519 key agreement, which is not
// an approved scheme under SP 800-56A Rev. 3. Under GODEBUG=fips140=only the Go
// Cryptographic Module refuses it outright, so the method cannot work; under
// GODEBUG=fips140=on the module permits it, and this package still declines to
// use it. That is the same call pkg/ceremony makes for an Ed25519 external
// officer and for the same reason: a deployment that has ASKED for FIPS mode
// should not be handed non-approved cryptography merely because the runtime
// tolerates it. Hence Enabled() rather than Enforced().
//
// The decision is taken at RUN time, not build time. go-truststrap also ships a
// "fips" build tag that compiles its X25519 helpers out, and choosing that would
// mean shipping two qrdb binaries whose enrollment surfaces differ and whose
// difference is invisible in the artifact. One binary that reads the runtime it
// was started in is the smaller claim, and it is the one GODEBUG itself makes.
//
// It is a variable for the reason readFileFn is one, and the reason is sharper
// here: the suite runs under GODEBUG=fips140=off in the ordinary CI job and
// under fips140=only in the nightly FIPS job, so a test that read the real
// runtime could assert only ONE of the two rulings in each job and the other
// would have to be skipped. Skips are forbidden in this suite
// (docs/seal/fips140-mode.md, "Handling a failure"), and a rule that is only
// ever exercised in one direction is how a selector regresses unnoticed.
// Overriding this lets a test assert the FIPS ruling in a non-FIPS runtime and
// the ordinary ruling in a FIPS one, wherever the assertion does not need a
// live X25519 handshake to make it.
var noiseAvailable = func() bool { return !fips140.Enabled() }

// NoiseAvailable reports whether the Noise NK enrollment method may be used in
// this process. It is false in FIPS 140-3 mode, where a first node advertises
// over the approved methods only and a join configured with Noise alone is
// refused.
//
// It is exported because the answer is a property of the RUNTIME rather than of
// any one call, and composers need it before they call anything: cmd/qrdbd to
// describe the enrollment surface it is about to stand up, go-xkms to decide
// what to tell an operator. Reading crypto/fips140 themselves would give them a
// different answer from this package's on GODEBUG=fips140=on, where the module
// permits X25519 and this package still declines it.
func NoiseAvailable() bool { return noiseAvailable() }

// NoiseFIPSUnavailableError is returned when an enrollment operation cannot
// proceed without the Noise NK method and this process is running in FIPS 140-3
// mode.
//
// It exists because the message an operator would otherwise get is actively
// misleading. go-truststrap's noiseproto wraps any key-derivation failure in
// ErrInvalidKeySize, so a FIPS refusal surfaces as
//
//	noise: invalid key size: crypto/ecdh: use of X25519 is not allowed in FIPS 140-only mode
//
// whose leading clause sends the reader after a key-length bug that is not
// there. The refusals in this package are raised BEFORE noiseproto is reached,
// so an operator in FIPS mode never sees that string; what they see names FIPS
// first and then names the approved methods to configure instead.
type NoiseFIPSUnavailableError struct {
	// Operation names what could not proceed without Noise, e.g.
	// "cluster enrollment join".
	Operation string
}

// Error returns a human-readable description.
func (e *NoiseFIPSUnavailableError) Error() string {
	return "clusterenroll: FIPS 140-3 mode is enabled and " + e.Operation +
		" was configured with the Noise NK method as its only option; Noise NK performs an " +
		"X25519 handshake, which is not a FIPS 140-3 approved key-agreement scheme. " +
		"Configure an approved bootstrap method instead -- SPKI-pinned TLS " +
		"(--bootstrap-spki-pin with --bootstrap-addr), DANE (--bootstrap-dane-dns) or " +
		"direct HTTPS (--bootstrap-direct-url) -- or start this node without FIPS 140-3 mode"
}

// fipsNoiseAdvisory is the sentence both the advertise and the join side log
// when Noise is dropped in FIPS mode. It is one constant so that an operator
// grepping their logs finds the same wording from either end of the enrollment.
const fipsNoiseAdvisory = "FIPS 140-3 mode: the Noise NK enrollment method is unavailable " +
	"because its X25519 handshake is not FIPS 140-3 approved"
