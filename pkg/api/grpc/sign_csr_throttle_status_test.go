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

package grpc

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A throttled ceremony-sign-csr is RESOURCE_EXHAUSTED on the gRPC and Unix
// service (mapServiceError, which CeremonySignCSR calls) and through the
// shared classifier MCP renders.
func TestMapServiceError_SignCSRThrottled(t *testing.T) {
	err := &service.SignCSRThrottledError{RetryAfter: time.Second}
	if got := status.Code(mapServiceError(err)); got != codes.ResourceExhausted {
		t.Fatalf("mapServiceError code = %v, want ResourceExhausted", got)
	}
	if got := ServiceErrorCode(err); got != codes.ResourceExhausted {
		t.Fatalf("ServiceErrorCode = %v, want ResourceExhausted", got)
	}
}
