/*
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Retry utility for transient SDK errors.
 *
 * Provides exponential backoff retry logic for operations that may fail
 * due to Raft leader election, connection issues, or other transient
 * conditions.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Configuration for retry behavior. */
export interface RetryOptions {
  /** Maximum number of retry attempts (default: 3). */
  readonly maxRetries?: number;
  /** Base delay in milliseconds before the first retry (default: 500). */
  readonly baseDelayMs?: number;
  /** Maximum delay cap in milliseconds (default: 5000). */
  readonly maxDelayMs?: number;
  /** Jitter factor (0-1) applied to each delay (default: 0.25). */
  readonly jitter?: number;
}

// ---------------------------------------------------------------------------
// Transient error detection
// ---------------------------------------------------------------------------

/** Patterns that indicate a retryable transient error. */
const TRANSIENT_PATTERNS: readonly string[] = [
  "not leader",
  "leader unknown",
  "timeout",
  "unavailable",
  "connection refused",
  "temporarily unavailable",
  "system is closed",
  "context deadline exceeded",
  "no connection",
] as const;

/**
 * Returns true if the error message indicates a transient failure that
 * may resolve on retry (e.g. Raft leader election in progress).
 */
export function isTransientError(err: unknown): boolean {
  const message = err instanceof Error ? err.message : String(err);
  const lower = message.toLowerCase();
  return TRANSIENT_PATTERNS.some((pattern) => lower.includes(pattern));
}

// ---------------------------------------------------------------------------
// Retry execution
// ---------------------------------------------------------------------------

/** Default retry configuration values. */
const DEFAULTS: Required<RetryOptions> = {
  maxRetries: 3,
  baseDelayMs: 500,
  maxDelayMs: 5000,
  jitter: 0.25,
} as const;

/**
 * Executes `fn` with exponential backoff retry for transient errors.
 *
 * Non-transient errors are thrown immediately without retry. The delay
 * between retries doubles each time, capped at `maxDelayMs`, with
 * random jitter applied to prevent thundering herd.
 *
 * @param fn      - The async operation to attempt.
 * @param options - Optional retry configuration overrides.
 * @returns The result of the first successful invocation.
 * @throws The last error if all retries are exhausted, or a non-transient error.
 */
export async function withRetry<T>(
  fn: () => Promise<T>,
  options?: RetryOptions,
): Promise<T> {
  const opts: Required<RetryOptions> = { ...DEFAULTS, ...options };
  let lastError: unknown;

  for (let attempt = 0; attempt <= opts.maxRetries; attempt++) {
    try {
      return await fn();
    } catch (err: unknown) {
      lastError = err;

      // Non-transient errors should not be retried.
      if (!isTransientError(err)) {
        throw err;
      }

      // Exhausted all retries.
      if (attempt >= opts.maxRetries) {
        break;
      }

      // Exponential backoff with jitter.
      const exponential = opts.baseDelayMs * Math.pow(2, attempt);
      const capped = Math.min(exponential, opts.maxDelayMs);
      const jitterRange = capped * opts.jitter;
      const jitterOffset = (Math.random() * 2 - 1) * jitterRange;
      const delay = Math.max(0, capped + jitterOffset);

      await sleep(delay);
    }
  }

  throw lastError;
}

/**
 * Returns a promise that resolves after the given number of milliseconds.
 */
function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
