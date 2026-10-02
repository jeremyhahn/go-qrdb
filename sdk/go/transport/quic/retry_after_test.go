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

package quic

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapWireError_CarriesTheRetryDelay: an error frame whose payload names
// a delay reaches the caller as a RequestError whose RetryAfter is that exact
// delay; a payload naming none leaves it zero.
func TestMapWireError_CarriesTheRetryDelay(t *testing.T) {
	body, err := wire.EncodeErrorPayload(wire.StatusResourceExhausted, wire.ErrorPayload{
		Message: "SO PIN locked", RetryAfter: 1500 * time.Millisecond, HasRetryAfter: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var reqErr *transport.RequestError
	if got := mapWireError("barrier.RekeyStart", wire.StatusResourceExhausted, body); !errors.As(got, &reqErr) || reqErr.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("mapped = %v, want a RequestError with RetryAfter 1.5s", got)
	}
	if got := mapWireError("barrier.RekeyStart", wire.StatusResourceExhausted, errorBody(t, wire.StatusResourceExhausted, "busy")); !errors.As(got, &reqErr) || reqErr.RetryAfter != 0 {
		t.Fatalf("mapped = %v, want RetryAfter 0 for a payload naming no delay", got)
	}
}
