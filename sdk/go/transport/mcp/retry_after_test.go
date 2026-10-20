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

package mcp

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapToolError_CarriesTheRetryDelay: an error envelope's
// retry_after_seconds reaches the caller as RequestError.RetryAfter; an
// envelope naming none leaves it zero.
func TestMapToolError_CarriesTheRetryDelay(t *testing.T) {
	var reqErr *transport.RequestError
	got := mapToolError("qrdb_barrier_rekey_start", &wire.ErrorPayload{Code: wire.CodeResourceExhausted, Message: "SO PIN locked", RetryAfterSeconds: 2})
	if !errors.As(got, &reqErr) || reqErr.RetryAfter != 2*time.Second {
		t.Fatalf("mapped = %v, want a RequestError with RetryAfter 2s", got)
	}
	got = mapToolError("qrdb_barrier_rekey_start", &wire.ErrorPayload{Code: wire.CodeResourceExhausted, Message: "busy"})
	if !errors.As(got, &reqErr) || reqErr.RetryAfter != 0 {
		t.Fatalf("mapped = %v, want RetryAfter 0 for an envelope naming no delay", got)
	}
}

// The unavailable envelope a create of a not-yet-released undone id answers
// keeps its delay.
func TestMapToolError_UndoPendingIsRetryableUnavailable(t *testing.T) {
	var reqErr *transport.RequestError
	got := mapToolError("qrdb_tenant_create", &wire.ErrorPayload{Code: wire.CodeUnavailable, Message: "tenant: id acme is pending the undo cleanup", RetryAfterSeconds: 5})
	if !errors.As(got, &reqErr) || reqErr.RetryAfter != 5*time.Second {
		t.Fatalf("mapped = %v, want a RequestError with RetryAfter 5s", got)
	}
}
