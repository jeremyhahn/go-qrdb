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

import "github.com/jeremyhahn/go-qrdb/pkg/authfactors"

// ---------------------------------------------------------------------------
// Auth factor registry (pkg/authfactors)
//
// These types come from pkg/authfactors, the pluggable factor-registry layer,
// distinct from the pkg/auth login-resolution types re-exported in
// types_authfactor.go.
// ---------------------------------------------------------------------------

// AuthFactorRegistry holds the registered pluggable authentication factors.
type AuthFactorRegistry = authfactors.FactorRegistry

// AuthFactorPlugin is a single pluggable authentication factor. (Aliases
// pkg/authfactors.Factor.)
type AuthFactorPlugin = authfactors.Factor

// AuthFactorEnrollParams carries the inputs for enrolling a pluggable factor.
type AuthFactorEnrollParams = authfactors.EnrollParams

// AuthFactorEnrollment is the result of enrolling a pluggable factor.
type AuthFactorEnrollment = authfactors.Enrollment

// AuthFactorChallenge is a challenge issued by a pluggable factor.
type AuthFactorChallenge = authfactors.Challenge

// AuthFactorResponse is a response to a pluggable factor challenge.
type AuthFactorResponse = authfactors.Response

// FactorAlreadyRegisteredError is returned when registering a factor type that
// is already registered.
type FactorAlreadyRegisteredError = authfactors.FactorAlreadyRegisteredError

// NewFactorRegistry creates a new empty AuthFactorRegistry.
var NewFactorRegistry = authfactors.NewFactorRegistry

// ---------------------------------------------------------------------------
// Auth factor registry errors and ID helpers (pkg/authfactors)
// ---------------------------------------------------------------------------

// AuthFactorEnrollmentInvalidError is returned when enrollment input is
// invalid. (Aliases pkg/authfactors.EnrollmentInvalidError.)
type AuthFactorEnrollmentInvalidError = authfactors.EnrollmentInvalidError

// AuthFactorChallengeExpiredError is returned when a challenge has expired.
// (Aliases pkg/authfactors.ChallengeExpiredError.)
type AuthFactorChallengeExpiredError = authfactors.ChallengeExpiredError

// AuthFactorVerificationFailedError is returned when factor verification
// fails. (Aliases pkg/authfactors.VerificationFailedError.)
type AuthFactorVerificationFailedError = authfactors.VerificationFailedError

// AuthFactorHardwareUnavailableError is returned when the underlying hardware
// device (HSM, smart card reader, FIDO2 authenticator) cannot be reached.
// (Aliases pkg/authfactors.HardwareUnavailableError.)
//
// qrdb ships no Factor implementation: hardware verification is performed by
// an injected out-of-repo factor (go-xkms), which is therefore the only thing
// that can produce this condition. Both transports already map it to 503, but
// until go-qrdb#243 it was the one error on the Factor seam with no SDK
// surface, so no consumer could construct the error the handler was waiting
// for. Exported here beside its three siblings for that reason.
type AuthFactorHardwareUnavailableError = authfactors.HardwareUnavailableError

// ErrAuthFactorHardwareUnavailable is the sentinel form of the same condition,
// for a factor that has no per-device detail to report. Both transports match
// it alongside the struct type. (Aliases pkg/authfactors.ErrHardwareUnavailable.)
var ErrAuthFactorHardwareUnavailable = authfactors.ErrHardwareUnavailable

// NewAuthFactorEnrollmentID generates a new unique enrollment identifier.
var NewAuthFactorEnrollmentID = authfactors.NewEnrollmentID

// NewAuthFactorChallengeID generates a new unique challenge identifier.
var NewAuthFactorChallengeID = authfactors.NewChallengeID
