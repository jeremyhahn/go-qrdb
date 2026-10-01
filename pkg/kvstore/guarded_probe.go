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

import (
	"context"
	"errors"
)

// GuardedBatchProbeKey is the reserved key ProbeGuardedBatch guards on. The
// probe writes nothing, so the key is only ever read by the state machine's
// compare; it lives under ReservedPrefixSystem so no tenant can own it.
const GuardedBatchProbeKey = ReservedPrefixSystem + "capability_probe/guarded_batch"

// ProbeGuardedBatch reports whether s can actually honour
// PutAllIfAllUnchanged, rather than merely offering it by type.
//
// A guarded adapter over a transaction plane satisfies
// MultiGuardedBatchKVStore whenever the plane exists, but the state machine
// behind it decides at apply time whether it can evaluate a transaction at
// all. The probe asks it directly with a genuine no-op — one absent-guard and
// an empty write set, which changes nothing whether or not the guard holds —
// and classifies the answer three ways:
//
//   - nil error: capable (true, nil).
//   - an error matching ErrAtomicWriteUnsupported (GuardedBatchUnsupportedError
//     is how every guarded adapter reports a state machine without the
//     capability): definitively NOT capable (false, nil).
//   - any other error — no leader yet, a cancelled ctx, a transport failure —
//     is not evidence either way and is returned as-is (false, err). A caller
//     that caches the answer must not cache this one.
func ProbeGuardedBatch(ctx context.Context, s MultiGuardedBatchKVStore) (bool, error) {
	_, err := s.PutAllIfAllUnchanged(ctx, []Guard{{Key: GuardedBatchProbeKey}}, nil)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrAtomicWriteUnsupported):
		return false, nil
	default:
		return false, err
	}
}
