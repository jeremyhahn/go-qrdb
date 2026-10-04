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

package forward

import (
	"errors"
	"testing"
	"time"
)

// TestHTTPBinding_IsTheAssertionRESTAlwaysMade: an HTTP binding produces the
// exact assertion REST made before bindings were generalized.
func TestHTTPBinding_IsTheAssertionRESTAlwaysMade(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"v":1}`)
	got := HTTPBinding(method, path, body).Assertion("alice", "acme", nodeID, now)
	want := NewCallerAssertion("alice", "acme", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	if got != want {
		t.Fatalf("HTTP binding assertion = %+v; want %+v", got, want)
	}
}

// TestRPCBinding_RoundTripsAndRefusesEveryTamper: a gRPC/QUIC binding verifies
// for the request it names and is refused for another protocol, another
// method or another payload.
func TestRPCBinding_RoundTripsAndRefusesEveryTamper(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	payload := []byte{0x0a, 0x03, 'k', 'e', 'y'}
	b := RPCBinding("grpc", "/qrdb.v1.QRDBService/KVPut", payload)
	if b.Method != "grpc:/qrdb.v1.QRDBService/KVPut" || b.Path != "" {
		t.Fatalf("binding = %+v; want the protocol-qualified method and no path", b)
	}
	ab, sb, err := Sign(b.Assertion("alice", "acme", nodeID, now), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyBinding(ab, sb, cert, tdom, now, b)
	if err != nil || got.Subject != "alice" || got.TenantID != "acme" {
		t.Fatalf("VerifyBinding = %+v, %v; want alice/acme", got, err)
	}
	for name, other := range map[string]Binding{
		"protocol": RPCBinding("quic", "/qrdb.v1.QRDBService/KVPut", payload),
		"method":   RPCBinding("grpc", "/qrdb.v1.QRDBService/KVDelete", payload),
		"payload":  RPCBinding("grpc", "/qrdb.v1.QRDBService/KVPut", []byte("other")),
	} {
		var mismatch *ForwardAssertionBindingMismatchError
		if _, err := VerifyBinding(ab, sb, cert, tdom, now, other); !errors.As(err, &mismatch) {
			t.Errorf("%s swapped: VerifyBinding = %v; want a binding mismatch", name, err)
		}
	}
}
