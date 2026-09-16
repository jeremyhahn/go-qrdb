// Copyright 2026 Jeremy Hahn
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

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// This transport decodes the DOMAIN record's JSON — the kind is pkg/user's
// ordinal and a non-expiring binding's notAfter is the zero time.Time, which
// marshals as 0001-01-01T00:00:00Z. Both must come out as they do on every
// other transport: the label, and an empty expiry (go-qrdb#535).
func TestServerUserToTransport_DecodesCertBindings(t *testing.T) {
	var in serverUser
	if err := json.Unmarshal([]byte(`{"id":1,"certBindings":[
		{"thumbprint":"AAAA","kind":0,"notAfter":"0001-01-01T00:00:00Z"},
		{"thumbprint":"BBBB","kind":1,"notAfter":"2020-01-01T00:00:00Z"},
		{"thumbprint":"CCCC","kind":2,"notAfter":"2030-06-01T12:34:56.789Z"},
		{"thumbprint":"DDDD","kind":7,"notAfter":"0001-01-01T00:00:00Z"}]}`), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := serverUserToTransport(&in)
	want := []transport.CertBinding{
		{Thumbprint: "AAAA", Kind: "spki_thumbprint"},
		{Thumbprint: "BBBB", Kind: "full_cert_fingerprint", NotAfter: "2020-01-01T00:00:00Z"},
		{Thumbprint: "CCCC", Kind: "piv_attested", NotAfter: "2030-06-01T12:34:56Z"},
		// A kind this client does not know is reported as unknown, in the same
		// spelling every other transport uses for it.
		{Thumbprint: "DDDD", Kind: "unknown(7)"},
	}
	if len(out.CertBindings) != len(want) {
		t.Fatalf("CertBindings = %+v, want %+v", out.CertBindings, want)
	}
	for i := range want {
		if out.CertBindings[i] != want[i] {
			t.Errorf("CertBindings[%d] = %+v, want %+v", i, out.CertBindings[i], want[i])
		}
	}
}

func TestServerUserToTransport_NoCertBindingsIsNil(t *testing.T) {
	if out := serverUserToTransport(&serverUser{ID: 1}); out.CertBindings != nil {
		t.Fatalf("CertBindings = %+v, want nil", out.CertBindings)
	}
}
