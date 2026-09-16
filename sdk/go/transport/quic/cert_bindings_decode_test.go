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

package quic

import (
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The binding set decodes from the server's camelCase JSON in full, expired
// ones included (go-qrdb#535). Decoded from JSON rather than built as a struct
// so the field names on the wire are what is asserted.
func TestWireToUser_DecodesCertBindings(t *testing.T) {
	var w userWireResponse
	if err := json.Unmarshal([]byte(`{"id":1,"certBindings":[
		{"thumbprint":"AAAA","kind":"spki_thumbprint"},
		{"thumbprint":"BBBB","kind":"full_cert_fingerprint","notAfter":"2020-01-01T00:00:00Z"}]}`), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := wireToUser(&w)
	want := []transport.CertBinding{
		{Thumbprint: "AAAA", Kind: "spki_thumbprint"},
		{Thumbprint: "BBBB", Kind: "full_cert_fingerprint", NotAfter: "2020-01-01T00:00:00Z"},
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

func TestWireToUser_NoCertBindingsIsNil(t *testing.T) {
	if out := wireToUser(&userWireResponse{ID: 1}); out.CertBindings != nil {
		t.Fatalf("CertBindings = %+v, want nil", out.CertBindings)
	}
}
