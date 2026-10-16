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

package grpc

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func sealedStatusErr(t *testing.T) error {
	t.Helper()
	st, err := status.New(codes.Unavailable, sealedwindow.SealedMessage).WithDetails(&errdetails.ErrorInfo{
		Domain: sealedwindow.ErrorDomain,
		Reason: sealedwindow.ReasonSealed,
	})
	if err != nil {
		t.Fatalf("attach error info: %v", err)
	}
	return st.Err()
}

// A sealed node refuses with Unavailable carrying the qrdb SEALED reason; the
// SDK reports it as *transport.BarrierSealedError and the health probe counts
// the node as reachable.
func TestMapGRPCError_SealedRefusal(t *testing.T) {
	raw := sealedStatusErr(t)
	if err := mapGRPCError("Get", raw); !transport.IsBarrierSealed(err) {
		t.Fatalf("err = %T %v, want *transport.BarrierSealedError", err, err)
	}
	if !reachableButSealed(raw) {
		t.Fatal("a sealed refusal is not counted as reachable")
	}
}

// Plain Unavailable is a draining or unready node, not a sealed one.
func TestMapGRPCError_PlainUnavailableIsNotSealed(t *testing.T) {
	raw := status.Error(codes.Unavailable, "draining")
	if err := mapGRPCError("Get", raw); transport.IsBarrierSealed(err) {
		t.Fatalf("plain Unavailable reported as sealed: %v", err)
	}
	if reachableButSealed(raw) {
		t.Fatal("plain Unavailable counted as reachable-but-sealed")
	}
}
