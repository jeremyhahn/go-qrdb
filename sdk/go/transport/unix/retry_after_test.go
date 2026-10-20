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

package unix

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapGRPCError_CarriesTheRetryDelay: a status carrying a
// google.rpc.RetryInfo detail -- what the server attaches to a throttled
// request or an SO PIN lockout -- reaches the caller as a RequestError whose
// RetryAfter is that exact delay; a status naming none leaves it zero.
func TestMapGRPCError_CarriesTheRetryDelay(t *testing.T) {
	st, err := status.New(codes.ResourceExhausted, "SO PIN locked").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(1500 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	var reqErr *transport.RequestError
	if got := mapGRPCError("BarrierRekeyStart", st.Err()); !errors.As(got, &reqErr) || reqErr.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("mapped = %v, want a RequestError with RetryAfter 1.5s", got)
	}
	if got := mapGRPCError("BarrierRekeyStart", status.Error(codes.ResourceExhausted, "busy")); !errors.As(got, &reqErr) || reqErr.RetryAfter != 0 {
		t.Fatalf("mapped = %v, want RetryAfter 0 for a status naming no delay", got)
	}
}

// The Unavailable a create of a not-yet-released undone id answers keeps its
// code and its RetryInfo delay.
func TestMapGRPCError_UndoPendingIsRetryableUnavailable(t *testing.T) {
	st, err := status.New(codes.Unavailable, "tenant: id acme is pending the undo cleanup").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	var reqErr *transport.RequestError
	if got := mapGRPCError("CreateTenant", st.Err()); !errors.As(got, &reqErr) || reqErr.StatusCode != int(codes.Unavailable) || reqErr.RetryAfter != 5*time.Second {
		t.Fatalf("mapped = %v, want an Unavailable RequestError with RetryAfter 5s", got)
	}
}
