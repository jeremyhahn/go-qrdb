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

package retryinfo

import (
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestDelay reads the first RetryInfo past other details, and names no delay
// for a status without one or with an invalid one.
func TestDelay(t *testing.T) {
	st, err := status.New(codes.ResourceExhausted, "m").WithDetails(
		&errdetails.ErrorInfo{Reason: "LOCKED"},
		&errdetails.RetryInfo{RetryDelay: durationpb.New(2 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := Delay(st); got != 2*time.Second {
		t.Fatalf("Delay = %v, want 2s", got)
	}
	if got := Delay(status.New(codes.ResourceExhausted, "m")); got != 0 {
		t.Fatalf("no RetryInfo: Delay = %v, want 0", got)
	}
	invalid, err := status.New(codes.ResourceExhausted, "m").WithDetails(&errdetails.RetryInfo{RetryDelay: &durationpb.Duration{Seconds: 1, Nanos: -1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := Delay(invalid); got != 0 {
		t.Fatalf("invalid delay: Delay = %v, want 0", got)
	}
}
