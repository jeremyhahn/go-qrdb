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

// ShamirProtectedShare represents a single piece of a secret split using
// Shamir's Secret Sharing scheme, its value held in protected memory.
type ShamirProtectedShare = shamir.ProtectedShare

// ---------------------------------------------------------------------------
// Shamir error types
// ---------------------------------------------------------------------------

// ShamirSplitError is returned when splitting a secret fails.
type ShamirSplitError = shamir.SplitError

// ShamirCombineError is returned when combining shares fails.
type ShamirCombineError = shamir.CombineError

// ShamirValidationError is returned when share validation fails.
type ShamirValidationError = shamir.ValidationError

// ---------------------------------------------------------------------------
// Shamir functions
// ---------------------------------------------------------------------------

// ShamirVerifyShare checks if a share is valid and consistent with other
// shares.
var ShamirVerifyShare = shamir.VerifyShare
