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

// The MCP server now projects CertBindings through user.WireCertBindings
// before marshaling (go-qrdb#192/#197), exactly as REST and QUIC do, rather
// than marshaling the domain *user.User directly. The kind therefore arrives
// as its wire label, not pkg/user's ordinal, and a non-expiring binding's
// notAfter is already omitted rather than the zero time.Time's
// 0001-01-01T00:00:00Z (go-qrdb#535's fix, now shared by this transport too).
func TestServerUserToTransport_DecodesCertBindings(t *testing.T) {
	var in serverUser
	if err := json.Unmarshal([]byte(`{"id":1,"certBindings":[
		{"thumbprint":"AAAA","kind":"spki_thumbprint"},
		{"thumbprint":"BBBB","kind":"full_cert_fingerprint","notAfter":"2020-01-01T00:00:00Z"},
		{"thumbprint":"CCCC","kind":"piv_attested","notAfter":"2030-06-01T12:34:56Z"},
		{"thumbprint":"DDDD","kind":"unknown(7)"}]}`), &in); err != nil {
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
