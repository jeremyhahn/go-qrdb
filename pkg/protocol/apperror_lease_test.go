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

package protocol

import (
	"strconv"
	"testing"
)

func TestLeaseNotFoundError_RoundTrips(t *testing.T) {
	for _, id := range []int64{1, 42, 1 << 62} {
		got, ok := DecodeLeaseNotFoundError(EncodeLeaseNotFoundError(id))
		if !ok || got != id {
			t.Fatalf("decode(encode(%d)) = %d, %v", id, got, ok)
		}
	}
}

// A generic reader that knows nothing of the lease marker still sees an
// application error, of the invalid kind, naming the lease.
func TestLeaseNotFoundError_GenericDecodeIsInvalid(t *testing.T) {
	msg, kind, ok := DecodeAppErrorKind(EncodeLeaseNotFoundError(7))
	if !ok || kind != AppErrorInvalid || msg != "lease not found: 7" {
		t.Fatalf("generic decode = %q, %v, %v; want the invalid kind naming lease 7", msg, kind, ok)
	}
}

func TestLeaseNotFoundError_RejectsOtherPayloads(t *testing.T) {
	malformed := append(append(append([]byte{}, appErrorPrefix...), leaseNotFoundMarker...), "seven"...)
	for name, data := range map[string][]byte{
		"plain result":      {1},
		"other refusal":     EncodeAppError(AppErrorInvalid, "lease not found: "+strconv.Itoa(7)),
		"non-numeric lease": malformed,
		"empty":             nil,
	} {
		if id, ok := DecodeLeaseNotFoundError(data); ok {
			t.Errorf("%s decoded as a lease-not-found refusal for %d", name, id)
		}
	}
}
