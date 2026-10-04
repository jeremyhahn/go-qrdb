// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kvstore

import "context"

// CompareAndSwapWithCurrent calls store.CompareAndSwap(ctx, key, expected,
// next) and, on a mismatch (swapped=false, err=nil), reads key back so the
// caller learns what it should have compared against without a second,
// separately-written round trip. A contended counter -- a PIN-lockout
// attempt count, a rate-limit window -- retries on every CAS failure, and
// "re-read, recompute next, try again" is that whole loop's body; this
// collapses the re-read into the same call that discovered the failure
// (go-qrdb#589).
//
// The read is NOT part of the same atomic step as the compare: current is a
// best-effort snapshot taken immediately after the failed compare, and key
// can change again before the caller acts on it. That is not a regression
// from calling CompareAndSwap and Get separately -- it is the same window,
// just inside one function instead of at the caller's call site -- and a
// caller whose retry also loses the race simply calls this again. No
// AtomicKVStore implementation in this tree offers a server-evaluated
// "compare, and return the current value atomically with the compare
// itself" primitive to collapse the window further; doing so would need a
// new verb on the capability interface and a wire change on every transport
// that implements it, not a composition of the two that already exist.
//
// current is nil when key is absent, consistent with KVStore.Get's own
// "(nil, nil) means absent" contract, and nil when swapped is true: a
// successful swap needs no current value, since the caller already knows
// what it just wrote. When the follow-up Get itself fails, current is nil
// and the Get's error is returned rather than silently discarded.
func CompareAndSwapWithCurrent(ctx context.Context, store AtomicKVStore, key string, expected, next []byte) (swapped bool, current []byte, err error) {
	swapped, err = store.CompareAndSwap(ctx, key, expected, next)
	if err != nil || swapped {
		return swapped, nil, err
	}

	current, err = store.Get(ctx, key)
	if err != nil {
		return false, nil, err
	}
	return false, current, nil
}
