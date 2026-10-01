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

package unix

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The binding set decodes in full, expired ones included, with the expiry
// rendered RFC 3339 and a non-expiring binding rendered with none
// (go-qrdb#535).
func TestProtoToUser_DecodesCertBindings(t *testing.T) {
	out := protoToUser(&pb.UserResponse{Id: 1, CertBindings: []*pb.CertBinding{
		{Thumbprint: "AAAA", Kind: "spki_thumbprint"},
		{Thumbprint: "BBBB", Kind: "piv_attested", NotAfterUnix: 1577836800}, // 2020-01-01T00:00:00Z
	}})
	want := []transport.CertBinding{
		{Thumbprint: "AAAA", Kind: "spki_thumbprint"},
		{Thumbprint: "BBBB", Kind: "piv_attested", NotAfter: "2020-01-01T00:00:00Z"},
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

func TestProtoToUser_NoCertBindingsIsNil(t *testing.T) {
	if out := protoToUser(&pb.UserResponse{Id: 1}); out.CertBindings != nil {
		t.Fatalf("CertBindings = %+v, want nil", out.CertBindings)
	}
}
