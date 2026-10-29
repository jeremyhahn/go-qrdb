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

package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// aliveSOPINs records the PIN SetSOPIN receives and whether the Secret it
// came from was still alive during the call.
type aliveSOPINs struct {
	fakeSOPINs
	secret      *secure.Secret
	aliveInCall bool
}

func (a *aliveSOPINs) SetSOPIN(ctx context.Context, tenantID string, pin []byte) error {
	a.aliveInCall = a.secret.IsAlive()
	return a.fakeSOPINs.SetSOPIN(ctx, tenantID, pin)
}

var _ SOPINSetter = (*aliveSOPINs)(nil)

func TestProvisionTenant_SO_PINReachesTheSeamByteIdenticalThenIsDestroyed(t *testing.T) {
	so := soConfig(t)
	pins := &aliveSOPINs{secret: so.PIN}
	if _, err := provisionWithSO(t, so, &fakeCertBindingUsers{}, Dependencies{SOPINs: pins}); err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(pins.pins) != 1 || string(pins.pins[0]) != "so-pin-1234" {
		t.Fatalf("SetSOPIN pins = %q, want exactly [\"so-pin-1234\"]", pins.pins)
	}
	if !pins.aliveInCall {
		t.Fatal("the PIN's Secret was destroyed before SetSOPIN ran")
	}
	if so.PIN.IsAlive() {
		t.Fatal("the committed PIN's Secret is still alive; want it destroyed")
	}
}

func TestProvisionTenant_SO_PINDestroyedWhenTheCommitFails(t *testing.T) {
	so := soConfig(t)
	cause := errors.New("seam down")
	_, err := provisionWithSO(t, so, &fakeCertBindingUsers{}, Dependencies{SOPINs: &fakeSOPINs{err: cause}})
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &enrol) || enrol.Op != "PIN commit" || !errors.Is(err, cause) {
		t.Fatalf("err = %v, want *TenantSOEnrollmentError{PIN commit} wrapping the seam's error", err)
	}
	if so.PIN.IsAlive() {
		t.Fatal("the PIN's Secret is still alive after a failed commit; want it destroyed")
	}
}

func TestProvisionTenant_SO_PINDestroyedOnARefusalBeforeThePlan(t *testing.T) {
	so := soConfig(t)
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme", SO: so}, Dependencies{})
	if !errors.As(err, new(*MissingLoggerError)) {
		t.Fatalf("err = %v, want *MissingLoggerError", err)
	}
	if so.PIN.IsAlive() {
		t.Fatal("the PIN's Secret is still alive after a refusal; want it destroyed")
	}
}

func TestPlanSO_RefusesADestroyedPIN(t *testing.T) {
	so := soConfig(t)
	so.PIN.Destroy()
	_, err := planSO(TenantProvisionConfig{TenantID: "acme", SO: so}, Dependencies{SOPINs: &fakeSOPINs{}}, AdminEnrollCert, "admin", &adminPlan{}, false)
	if !isIncomplete("so_pin")(err) {
		t.Fatalf("planSO(destroyed PIN) = %v, want the so_pin refusal", err)
	}
}

func TestCommitSOPIN_PassesThePINAndWrapsAFailure(t *testing.T) {
	pins := &fakeSOPINs{}
	cfg := TenantProvisionConfig{TenantID: "acme"}
	if err := commitSOPIN(context.Background(), cfg, Dependencies{SOPINs: pins}, sealedPIN(t, "pin-a")); err != nil {
		t.Fatalf("commitSOPIN: %v", err)
	}
	if len(pins.pins) != 1 || string(pins.pins[0]) != "pin-a" || pins.tenants[0] != "acme" {
		t.Fatalf("SetSOPIN got tenants %q pins %q, want acme and pin-a", pins.tenants, pins.pins)
	}

	dead := sealedPIN(t, "pin-b")
	dead.Destroy()
	untouched := &fakeSOPINs{}
	err := commitSOPIN(context.Background(), cfg, Dependencies{SOPINs: untouched}, dead)
	var enrol *TenantSOEnrollmentError
	if !errors.As(err, &enrol) || enrol.Op != "PIN commit" {
		t.Fatalf("commitSOPIN(destroyed) = %v, want *TenantSOEnrollmentError{PIN commit}", err)
	}
	if len(untouched.pins) != 0 {
		t.Fatalf("SetSOPIN called %d times with a destroyed PIN, want 0", len(untouched.pins))
	}
}
