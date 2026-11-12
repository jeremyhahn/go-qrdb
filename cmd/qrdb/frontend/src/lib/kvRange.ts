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
 * Key-range helpers for `MVCCClient.range()` prefix scans.
 *
 * `client.mvcc.range(start, end, opts)` takes an explicit half-open
 * `[start, end)` bound -- there is no "prefix" convenience on the versioned
 * range API the way there is for `client.kv.scan(prefix)`. These helpers
 * mirror the same "increment the last unit, carry on overflow" algorithm the
 * CLI (`cmd/qrdb/kv.go`'s `prefixEnd`) and the server
 * (`pkg/service/mvcc_service.go`'s `prefixEnd`) use for the equivalent byte
 * slices, adapted to the SDK's string-typed (UTF-8 text) public key API: it
 * increments the last Unicode code point of the prefix rather than the last
 * byte. Real-world text keys never approach the code point ceiling, so this
 * is exact in practice; see `rangeBoundsForPrefix` for the empty-prefix case.
 */

/**
 * Returns the exclusive upper bound of the half-open range `[prefix, end)`
 * that covers every key beginning with `prefix`, or `undefined` when
 * `prefix` has no representable successor (every code point is already at
 * the maximum, U+10FFFF -- effectively unreachable for real keys).
 */
export function prefixEnd(prefix: string): string | undefined {
  const codePoints = Array.from(prefix);
  for (let i = codePoints.length - 1; i >= 0; i--) {
    const unit = codePoints[i];
    if (unit === undefined) continue;
    const cp = unit.codePointAt(0);
    if (cp === undefined) continue;
    if (cp < 0x10ffff) {
      const carried = [...codePoints.slice(0, i), String.fromCodePoint(cp + 1)];
      return carried.join("");
    }
  }
  return undefined;
}

/**
 * Sentinel bounds spanning the full keyspace, used when no prefix filter is
 * set. `client.mvcc.range` requires a non-empty `start` (an empty string
 * means "not provided" on the REST wire, matching `/kv/range`'s query-param
 * contract), and the server's own equivalent (`pkg/service/mvcc_service.go`)
 * uses the raw byte `0x00` for "beginning of keyspace". A literal NUL
 * character survives less reliably across URL/JSON encoders and browser
 * `fetch` implementations than it does as a raw Go byte, so U+0020 (space)
 * is used here instead: it sorts below every printable key an operator would
 * realistically type, at the cost of not matching keys that legitimately
 * begin with an ASCII control character. The end sentinel, the highest valid
 * Unicode code point, sorts after any realistic text key.
 */
export const KEYSPACE_START = " ";
export const KEYSPACE_END = "\u{10FFFF}";

/** The `[start, end)` bounds for an MVCC range query. */
export interface RangeBounds {
  readonly start: string;
  readonly end: string;
}

/**
 * Returns the `[start, end)` bounds for an MVCC range query scoped to
 * `prefix`, or the full keyspace when `prefix` is empty.
 */
export function rangeBoundsForPrefix(prefix: string): RangeBounds {
  if (prefix === "") {
    return { start: KEYSPACE_START, end: KEYSPACE_END };
  }
  return { start: prefix, end: prefixEnd(prefix) ?? KEYSPACE_END };
}
