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

package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestGRPC_NilRequest_Refused pins that each call refuses a nil request with
// a RequestError wrapping NilRequestError rather than dereferencing it.
func TestGRPC_NilRequest_Refused(t *testing.T) {
	tr := connectWireServer(t, nil)
	ctx := context.Background()
	cases := map[string]func() error{
		"UpdatePool": func() error { _, err := tr.UpdatePool(ctx, nil); return err },
		"ShareClaimBegin": func() error {
			_, err := tr.ShareClaimBegin(ctx, nil)
			return err
		},
		"ShareClaimComplete": func() error {
			_, err := tr.ShareClaimComplete(ctx, nil)
			return err
		},
		"RecoveryShareClaimBegin": func() error {
			_, err := tr.RecoveryShareClaimBegin(ctx, nil)
			return err
		},
		"RecoveryShareClaimComplete": func() error {
			_, err := tr.RecoveryShareClaimComplete(ctx, nil)
			return err
		},
	}
	for method, call := range cases {
		t.Run(method, func(t *testing.T) {
			err := call()
			var nre *transport.NilRequestError
			var re *transport.RequestError
			if !errors.As(err, &re) || re.Method != method || !errors.As(err, &nre) {
				t.Fatalf("%s(nil) = %T %v, want RequestError wrapping NilRequestError", method, err, err)
			}
		})
	}
}

// TestGRPC_NilRequest_ClosedWins pins that a closed transport reports
// ClosedError ahead of the nil-request refusal.
func TestGRPC_NilRequest_ClosedWins(t *testing.T) {
	tr := connectWireServer(t, nil)
	tr.closed.Store(true)
	var ce *transport.ClosedError
	if _, err := tr.UpdatePool(context.Background(), nil); !errors.As(err, &ce) {
		t.Fatalf("UpdatePool(nil) on a closed transport = %T %v, want ClosedError", err, err)
	}
	if _, err := tr.ShareClaimBegin(context.Background(), nil); !errors.As(err, &ce) {
		t.Fatalf("ShareClaimBegin(nil) on a closed transport = %T %v, want ClosedError", err, err)
	}
}
