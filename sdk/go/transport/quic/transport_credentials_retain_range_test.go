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

package quic

import (
	"context"
	"errors"
	"math"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTransport_CredentialSetRotationPolicy_RetainVersionsRange pins that a
// retention count outside int32 is rejected before narrowing, so it never
// reaches the server as a wrapped count.
func TestTransport_CredentialSetRotationPolicy_RetainVersionsRange(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("credential.SetRotationPolicy", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})

	for _, tc := range []struct {
		name   string
		count  int
		reject bool
	}{
		{"below MinInt32", math.MinInt32 - 1, true},
		{"wraps to 1", -4294967295, true},
		{"above MaxInt32", math.MaxInt32 + 1, true},
		{"in range", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
				Name: "db-cred", Enabled: true, Interval: "24h", RetainVersions: tc.count,
			})
			rec := srv.LastRequest("credential.SetRotationPolicy")
			if !tc.reject {
				if err != nil || rec == nil {
					t.Fatalf("err = %v, request sent = %v; want nil and a request", err, rec != nil)
				}
				var sent pb.CredentialSetRotationPolicyRequest
				if err := proto.Unmarshal(rec.Payload, &sent); err != nil {
					t.Fatalf("request payload: %v", err)
				}
				if sent.GetRetainVersions() != int32(tc.count) {
					t.Fatalf("sent retain versions = %d, want %d", sent.GetRetainVersions(), tc.count)
				}
				return
			}
			var re *transport.RetainVersionsRangeError
			if !errors.As(err, &re) || re.Count != tc.count {
				t.Fatalf("err = %v, want *RetainVersionsRangeError for %d", err, tc.count)
			}
			if rec != nil {
				t.Fatalf("a request reached the server for rejected count %d", tc.count)
			}
		})
	}
}
