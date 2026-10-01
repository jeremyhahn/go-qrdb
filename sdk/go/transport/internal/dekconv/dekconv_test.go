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

package dekconv

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

func TestStatusConvertsLiveAndRetiredVersions(t *testing.T) {
	got := Status(&pb.TenantDEKStatusResponse{
		TenantId: "acme", Algorithm: "AES-256-GCM", CurrentVersion: 2,
		Versions: []*pb.TenantDEKVersion{
			{Version: 1, CreatedAtUnix: 100, RetiredAtUnix: 200},
			{Version: 2, CreatedAtUnix: 150},
		},
	})
	if got.TenantID != "acme" || got.CurrentVersion != 2 || len(got.Versions) != 2 {
		t.Fatalf("Status = %+v", got)
	}
	if got.Versions[0].RetiredAt == nil || got.Versions[0].RetiredAt.Unix() != 200 {
		t.Fatalf("v1 retired_at = %v", got.Versions[0].RetiredAt)
	}
	if got.Versions[1].RetiredAt != nil || got.Versions[1].CreatedAt.Unix() != 150 {
		t.Fatalf("v2 = %+v", got.Versions[1])
	}
}

// TestNilResponsesConvertToZeroValues: protobuf getters are nil-safe, so a
// nil response converts to an empty result instead of panicking.
func TestNilResponsesConvertToZeroValues(t *testing.T) {
	if st := Status(nil); st.CurrentVersion != 0 || st.Versions == nil {
		t.Fatalf("Status(nil) = %+v", st)
	}
	if e := Encrypt(nil); e.Ciphertext != nil || e.Version != 0 {
		t.Fatalf("Encrypt(nil) = %+v", e)
	}
	if d := Decrypt(nil); d.Plaintext != nil {
		t.Fatalf("Decrypt(nil) = %+v", d)
	}
	if r := Rewrap(&pb.TenantDEKRewrapResponse{Ciphertext: []byte("c"), FromVersion: 1, ToVersion: 3}); r.FromVersion != 1 || r.ToVersion != 3 {
		t.Fatalf("Rewrap = %+v", r)
	}
}
