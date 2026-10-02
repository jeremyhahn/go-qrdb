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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbedded_LockoutCarriesTheRetryDelay: in process, an SO PIN lockout
// reaches the caller as a RequestError whose RetryAfter is the lockout's own
// delay, as it does over every network transport; the refusal's type is still
// reachable through it, and a refusal naming no delay is not wrapped.
func TestEmbedded_LockoutCarriesTheRetryDelay(t *testing.T) {
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", rekeySOPIN); err != nil {
		t.Fatal(err)
	}
	tr := newRekeyTransport(t, pins)
	ctx := context.Background()
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: "wrong-pin-0000"})
		var reqErr *transport.RequestError
		if errors.As(err, &reqErr) {
			t.Fatalf("wrong PIN %d = %v; a refusal naming no delay must not be wrapped", i+1, err)
		}
	}
	_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: rekeySOPIN})
	var reqErr *transport.RequestError
	var locked *pin.SOPINLockedError
	if !errors.As(err, &reqErr) || !errors.As(err, &locked) {
		t.Fatalf("locked out = %v, want a RequestError around *pin.SOPINLockedError", err)
	}
	if reqErr.RetryAfter <= 0 || reqErr.RetryAfter != locked.RetryAfter || reqErr.Method != "BarrierRekeyStart" {
		t.Fatalf("RequestError = %+v, want RetryAfter %v on BarrierRekeyStart", reqErr, locked.RetryAfter)
	}
}
