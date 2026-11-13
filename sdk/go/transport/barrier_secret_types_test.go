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

package transport

import (
	"encoding/json"
	"reflect"
	"testing"
)

// secretBearingField names one request field that carries caller-supplied
// key material, together with the JSON name it must keep on the wire.
type secretBearingField struct {
	req   any
	field string
	json  string
}

// barrierSecretFields is the closed list of barrier request fields that carry
// a caller-supplied credential. It is written out rather than discovered so
// that ADDING a secret-bearing field is a deliberate act that shows up in this
// list, not something a reflective walk quietly absorbs.
var barrierSecretFields = []secretBearingField{
	{req: BarrierInitializeRequest{}, field: "Secret", json: "secret"},
	{req: BarrierUnsealRequest{}, field: "Secret", json: "secret"},
	// A holder's share, recovered from its envelope, is submitted to a
	// quorum and must be wipeable by the caller.
	{req: QuorumSubmitRequest{}, field: "Share", json: "share"},
	{req: TenantQuorumSubmitRequest{}, field: "Share", json: "share"},
	{req: BarrierEnableEncryptionRequest{}, field: "Passphrase", json: "passphrase,omitempty"},
	{req: TenantBarrierInitRequest{}, field: "Passphrase", json: "passphrase,omitempty"},
	// Already []byte before this rule was written down; listed so a
	// regression here is caught by the same test as the rest.
	{req: TenantBarrierUnsealRequest{}, field: "Key", json: "key,omitempty"},
}

// TestBarrierSecretFieldsAreWipeableBytes is the guard on the property this
// package exists to provide the caller: a barrier credential is []byte, never
// string.
//
// A Go string is immutable, so a passphrase materialized as one CANNOT be
// zeroized — it survives on the heap until the collector happens to reuse the
// page, recoverable from a core dump or a memory scrape for an unbounded
// period. Every consumer holding a passphrase in protected memory would have
// to undo that protection just to populate the request.
//
// Reverting any field in barrierSecretFields to string fails this test.
func TestBarrierSecretFieldsAreWipeableBytes(t *testing.T) {
	byteSlice := reflect.TypeOf([]byte(nil))
	for _, tc := range barrierSecretFields {
		rt := reflect.TypeOf(tc.req)
		t.Run(rt.Name()+"."+tc.field, func(t *testing.T) {
			f, ok := rt.FieldByName(tc.field)
			if !ok {
				t.Fatalf("%s has no field %q", rt.Name(), tc.field)
			}
			if f.Type != byteSlice {
				t.Fatalf("%s.%s is %s; a secret-bearing field must be []byte so the "+
					"caller can zeroize it — a Go string cannot be wiped",
					rt.Name(), tc.field, f.Type)
			}
			if got := f.Tag.Get("json"); got != tc.json {
				t.Fatalf("%s.%s json tag = %q, want %q", rt.Name(), tc.field, got, tc.json)
			}
		})
	}
}

// TestBarrierSecretFieldsMarshalAsBase64 pins the REST wire encoding. A
// []byte marshals to base64 in JSON, which is the same convention every other
// []byte field in this package already uses, and which the server's decoder
// and the TypeScript SDK both depend on. A silent drift here would send the
// wrong bytes rather than fail loudly.
func TestBarrierSecretFieldsMarshalAsBase64(t *testing.T) {
	data, err := json.Marshal(&BarrierInitializeRequest{Secret: []byte("hunter2")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"secret":"aHVudGVyMg=="}`
	if string(data) != want {
		t.Fatalf("marshal = %s, want %s", data, want)
	}

	var round BarrierInitializeRequest
	if err := json.Unmarshal([]byte(want), &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(round.Secret) != "hunter2" {
		t.Fatalf("round-trip = %q, want %q", round.Secret, "hunter2")
	}
}

// TestBarrierSecretFieldsOmitEmpty documents which of the secret fields drop
// out of the encoded body when unset. secret is NOT omitempty on initialize
// and unseal because an empty secret is meaningful there — it is how the
// "none" strategy is requested — and omitting the key would make an
// explicitly-empty secret indistinguishable from a malformed body.
func TestBarrierSecretFieldsOmitEmpty(t *testing.T) {
	// #nosec G117 -- marshals an EMPTY request to assert which secret keys are omitted; no secret value is present.
	data, err := json.Marshal(&BarrierInitializeRequest{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"secret":null}` {
		t.Fatalf("empty initialize marshal = %s, want {\"secret\":null}", data)
	}

	data, err = json.Marshal(&BarrierEnableEncryptionRequest{Strategy: "shamir"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"strategy":"shamir"}` {
		t.Fatalf("empty passphrase marshal = %s, want {\"strategy\":\"shamir\"}", data)
	}
}
