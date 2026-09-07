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

import "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

// ---------------------------------------------------------------------------
// Shamir secret sharing types
// ---------------------------------------------------------------------------

// ShamirShare represents a single piece of a secret split using Shamir's
// Secret Sharing scheme.
type ShamirShare = shamir.Share

// ---------------------------------------------------------------------------
// Shamir error types
// ---------------------------------------------------------------------------

// ShamirSplitError is returned when splitting a secret fails.
type ShamirSplitError = shamir.SplitError

// ShamirCombineError is returned when combining shares fails.
type ShamirCombineError = shamir.CombineError

// ShamirValidationError is returned when share validation fails.
type ShamirValidationError = shamir.ValidationError

// ShamirIntegrityError is an alias of shamir.IntegrityError.
//
// Deprecated: shares no longer carry a secret digest (it was an offline
// brute-force oracle), so combining performs no self-verification and never
// returns this error. Authenticate the reconstructed secret out-of-band.
//
//nolint:staticcheck // SA1019: re-exporting the deprecated type is this alias's purpose.
type ShamirIntegrityError = shamir.IntegrityError

// ---------------------------------------------------------------------------
// Shamir functions
// ---------------------------------------------------------------------------

// ShamirSplit divides a secret into N shares where any M shares can
// reconstruct it.
//
// Deprecated: the shares this returns carry their value in an un-wipeable Go
// string. Upstream's replacement is shamir.SplitProtected, whose shares hold
// their values in mlock'd, guard-paged buffers that Destroy wipes; the SDK does
// not re-export it yet, so reach it through go-quicraft's package directly.
// Retained for consumers already compiled against it; nothing about the shares
// it produces has changed.
//
//nolint:staticcheck // SA1019: re-exporting the deprecated function is this var's purpose.
var ShamirSplit = shamir.Split

// ShamirCombine reconstructs the original secret from M or more shares.
//
// Deprecated: it reconstructs from shares whose values are un-wipeable Go
// strings and returns the secret in an ordinary heap buffer. Upstream's
// replacement is shamir.CombineProtected, which reads share values in place out
// of protected memory and returns the secret in a buffer the caller can wipe;
// the SDK does not re-export it yet, so reach it through go-quicraft's package
// directly. Retained for consumers already compiled against it.
//
//nolint:staticcheck // SA1019: re-exporting the deprecated function is this var's purpose.
var ShamirCombine = shamir.Combine

// ShamirVerifyShare checks if a share is valid and consistent with other
// shares.
var ShamirVerifyShare = shamir.VerifyShare
