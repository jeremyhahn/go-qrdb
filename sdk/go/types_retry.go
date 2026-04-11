// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/retry"
)

// ---------------------------------------------------------------------------
// Backoff interface and strategy types
// ---------------------------------------------------------------------------

// Backoff defines the interface for retry backoff strategies.
type Backoff = retry.Backoff

// ExponentialBackoff implements exponential backoff with optional jitter.
type ExponentialBackoff = retry.ExponentialBackoff

// LinearBackoff implements linear backoff with optional jitter.
type LinearBackoff = retry.LinearBackoff

// ConstantBackoff implements constant backoff with optional jitter.
type ConstantBackoff = retry.ConstantBackoff

// ---------------------------------------------------------------------------
// Retry config and operation types
// ---------------------------------------------------------------------------

// RetryConfig configures retry behavior including max attempts, backoff
// strategy, retry predicate, and an optional per-attempt callback.
type RetryConfig = retry.Config

// RetryOperation is a function that can be retried.
type RetryOperation = retry.Operation

// ---------------------------------------------------------------------------
// Default backoff constructors
// ---------------------------------------------------------------------------

// DefaultExponentialBackoff returns a preconfigured exponential backoff
// with sensible defaults: 100ms initial, 10s max, 2x multiplier, 0.1 jitter.
var DefaultExponentialBackoff = retry.DefaultExponentialBackoff

// DefaultLinearBackoff returns a preconfigured linear backoff
// with sensible defaults: 100ms initial, 5s max, 200ms increment, 0.1 jitter.
var DefaultLinearBackoff = retry.DefaultLinearBackoff

// DefaultConstantBackoff returns a preconfigured constant backoff
// with sensible defaults: 500ms delay, 0.1 jitter.
var DefaultConstantBackoff = retry.DefaultConstantBackoff

// DefaultRetryConfig returns a retry config with sensible defaults.
var DefaultRetryConfig = retry.DefaultConfig

// ---------------------------------------------------------------------------
// Retry execution functions
// ---------------------------------------------------------------------------

// RetryDo executes the operation with retry logic according to the config.
var RetryDo = retry.Do

// RetryDoWithBackoff retries an operation with the specified max attempts
// and backoff strategy.
var RetryDoWithBackoff = retry.DoWithBackoff

// RetryDoExponential retries an operation with exponential backoff.
var RetryDoExponential = retry.DoExponential

// RetryDoLinear retries an operation with linear backoff.
var RetryDoLinear = retry.DoLinear

// RetryDoConstant retries an operation with constant backoff.
var RetryDoConstant = retry.DoConstant
