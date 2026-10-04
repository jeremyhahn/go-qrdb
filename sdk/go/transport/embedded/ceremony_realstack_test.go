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

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequest_CeremonyInitialize_RealSOPINGateRefusesWrongPIN is the
// go-qrdb#560 review B embedded-transport equivalent of pkg/api/rest's
// TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN: unlike this
// package's own TestRequest_CeremonyDispatch* tests (a mockCeremonyService
// that never checks a PIN at all), this drives a REAL
// ceremony.CeremonyService, a REAL pin.SoftwareBackend and the REAL
// service.CeremonyServiceWithAdminRebind wrapper through the embedded
// transport's generic Request dispatch. Removing
// pin.RequireProvisionedSOPIN from ReissueAdminCredential (or reverting
// VerifyReissueSOPIN's ordering) turns this red; the mock-based siblings in
// this package cannot.
func TestRequest_CeremonyInitialize_RealSOPINGateRefusesWrongPIN(t *testing.T) {
	ctx := context.Background()
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	tr := newExtTransport(t, WithCeremonyService(wrapped), WithCeremonyCA(ca))

	// Wrong SO PIN: refused, never bound.
	var out *transport.CeremonyInitializeResponse
	err := tr.Request(ctx, "CeremonyInitialize",
		&transport.CeremonyInitializeRequest{SOPin: "wrong-pin", AdminGenerate: true}, &out)
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("wrong SO PIN: got %v, want pin.ErrPINInvalid", err)
	}

	// Missing SO PIN entirely: also refused.
	err = tr.Request(ctx, "CeremonyInitialize",
		&transport.CeremonyInitializeRequest{AdminGenerate: true}, &out)
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("missing SO PIN: got %v, want pin.ErrPINInvalid", err)
	}

	// The correct SO PIN still reissues and binds cleanly -- proving the
	// refusals above were about the PIN, not a stack that always refuses.
	err = tr.Request(ctx, "CeremonyInitialize",
		&transport.CeremonyInitializeRequest{SOPin: "correct-horse-battery", AdminGenerate: true}, &out)
	if err != nil {
		t.Fatalf("correct SO PIN: expected success, got %v", err)
	}
}

// TestRequest_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound
// is the OTHER half of the go-qrdb#560 review B ordering check, over the
// embedded transport: against an admin that IS already bound, a wrong SO
// PIN must still answer the PIN failure (pin.ErrPINInvalid), never
// service.AdminAlreadyBoundError's own distinct signal -- which would leak
// the admin's binding state to a caller who never authenticated at all.
func TestRequest_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound(t *testing.T) {
	ctx := context.Background()
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	tr := newExtTransport(t, WithCeremonyService(wrapped), WithCeremonyCA(ca))

	var out *transport.CeremonyInitializeResponse
	if err := tr.Request(ctx, "CeremonyInitialize",
		&transport.CeremonyInitializeRequest{SOPin: "correct-horse-battery", AdminGenerate: true}, &out); err != nil {
		t.Fatalf("bind: expected success, got %v", err)
	}

	err := tr.Request(ctx, "CeremonyInitialize",
		&transport.CeremonyInitializeRequest{SOPin: "wrong-pin", AdminGenerate: true}, &out)
	var alreadyBound *service.AdminAlreadyBoundError
	if errors.As(err, &alreadyBound) {
		t.Fatal("wrong SO PIN against an already-bound admin leaked AdminAlreadyBoundError instead of the PIN failure")
	}
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("got %v, want pin.ErrPINInvalid", err)
	}
}
