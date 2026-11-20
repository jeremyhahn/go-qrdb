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

package unix

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTransport_CredentialSetRotationPolicy_RetainVersionsRange pins that a
// retention count outside int32 is rejected before narrowing, so it never
// reaches the server as a wrapped count.
func TestTransport_CredentialSetRotationPolicy_RetainVersionsRange(t *testing.T) {
	calls := 0
	var got int
	svc := &mockCredentialSvc{
		setPolicyFn: func(_ context.Context, _ string, policy credentials.RotationPolicy) error {
			calls++
			got = policy.RetainVersions
			return nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

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
			before := calls
			err := tr.CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{
				Name: "db-password", Enabled: true, Interval: "720h", RetainVersions: tc.count,
			})
			if !tc.reject {
				if err != nil || calls != before+1 || got != tc.count {
					t.Fatalf("err = %v, calls %d->%d, server saw %d; want nil and %d", err, before, calls, got, tc.count)
				}
				return
			}
			var re *transport.RetainVersionsRangeError
			if !errors.As(err, &re) || re.Count != tc.count {
				t.Fatalf("err = %v, want *RetainVersionsRangeError for %d", err, tc.count)
			}
			if calls != before {
				t.Fatalf("server called for rejected count %d", tc.count)
			}
		})
	}
}
