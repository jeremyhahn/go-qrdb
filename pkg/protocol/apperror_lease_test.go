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
	"bytes"
	"encoding/binary"
	"runtime"
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

func TestLeaseInfo_RoundTrips(t *testing.T) {
	for name, tc := range map[string]struct {
		id, granted, remaining int64
		keys                   [][]byte
	}{
		"no keys":           {id: 42, granted: 60, remaining: 58},
		"keys":              {id: 7, granted: 600, remaining: 1, keys: [][]byte{[]byte("a"), []byte("key/two")}},
		"empty key":         {id: 1, granted: 5, remaining: 5, keys: [][]byte{{}}},
		"expired remaining": {id: 1 << 62, granted: 3600, remaining: -1},
	} {
		id, granted, remaining, keys, ok := DecodeLeaseInfo(EncodeLeaseInfo(tc.id, tc.granted, tc.remaining, tc.keys))
		if !ok || id != tc.id || granted != tc.granted || remaining != tc.remaining || len(keys) != len(tc.keys) {
			t.Fatalf("%s: decode = %d, %d, %d, %d keys, %v", name, id, granted, remaining, len(keys), ok)
		}
		for i := range keys {
			if !bytes.Equal(keys[i], tc.keys[i]) {
				t.Errorf("%s: key %d = %q, want %q", name, i, keys[i], tc.keys[i])
			}
		}
	}
}

// The decoded keys are copies: the caller may hold them after the apply's
// result buffer is reused.
func TestLeaseInfo_DecodedKeysDoNotAliasPayload(t *testing.T) {
	data := EncodeLeaseInfo(9, 10, 10, [][]byte{[]byte("abc")})
	_, _, _, keys, ok := DecodeLeaseInfo(data)
	if !ok {
		t.Fatal("decode failed")
	}
	for i := range data {
		data[i] = 0
	}
	if string(keys[0]) != "abc" {
		t.Fatalf("key = %q after the payload was overwritten, want %q", keys[0], "abc")
	}
}

func TestLeaseInfo_RejectsOtherPayloads(t *testing.T) {
	valid := EncodeLeaseInfo(3, 30, 30, [][]byte{[]byte("key")})
	for name, data := range map[string][]byte{
		"empty":                nil,
		"tag only":             {leaseInfoTag},
		"truncated header":     valid[:len(valid)-len("key")-4-1],
		"truncated key length": valid[:len(valid)-len("key")-2],
		"truncated key":        valid[:len(valid)-1],
		"trailing bytes":       append(append([]byte{}, valid...), 0),
		"keepalive not found":  EncodeLeaseKeepAliveNotFound(3),
		"application error":    EncodeLeaseNotFoundError(3),
	} {
		if _, _, _, _, ok := DecodeLeaseInfo(data); ok {
			t.Errorf("%s decoded as lease info", name)
		}
	}
}

// A key count the payload cannot hold is refused before anything is sized
// from it: the count is a 32-bit field, and trusting it would reserve up to
// 2^32 slice headers for a 29-byte payload.
func TestLeaseInfo_ForgedKeyCountAllocatesNothing(t *testing.T) {
	data := EncodeLeaseInfo(3, 30, 30, nil)
	binary.LittleEndian.PutUint32(data[len(data)-4:], 1<<24)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, _, _, ok := DecodeLeaseInfo(data)
	runtime.ReadMemStats(&after)
	if ok {
		t.Fatal("a payload claiming 2^24 keys and holding none decoded")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("decoding a forged key count allocated %d bytes", grew)
	}
}

func TestLeaseKeepAliveNotFound_RoundTrips(t *testing.T) {
	for _, id := range []int64{0, 1, 42, -5, 1 << 62} {
		got, ok := DecodeLeaseKeepAliveNotFound(EncodeLeaseKeepAliveNotFound(id))
		if !ok || got != id {
			t.Fatalf("decode(encode(%d)) = %d, %v", id, got, ok)
		}
	}
}

// Neither tagged result is an application error, and neither is the other.
func TestLeaseKeepAliveNotFound_RejectsOtherPayloads(t *testing.T) {
	valid := EncodeLeaseKeepAliveNotFound(11)
	for name, data := range map[string][]byte{
		"empty":             nil,
		"truncated":         valid[:len(valid)-1],
		"trailing bytes":    append(append([]byte{}, valid...), 0),
		"lease info":        EncodeLeaseInfo(11, 1, 1, nil),
		"application error": EncodeLeaseNotFoundError(11),
	} {
		if id, ok := DecodeLeaseKeepAliveNotFound(data); ok {
			t.Errorf("%s decoded as a keepalive not-found for %d", name, id)
		}
	}
	for name, data := range map[string][]byte{
		"keepalive not found": valid,
		"lease info":          EncodeLeaseInfo(11, 1, 1, nil),
	} {
		if _, _, ok := DecodeAppErrorKind(data); ok {
			t.Errorf("%s decoded as an application error", name)
		}
	}
}
