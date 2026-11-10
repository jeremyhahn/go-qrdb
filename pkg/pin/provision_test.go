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

package pin

import (
	"context"
	"errors"
	"testing"
)

func newProvisionBackend(t *testing.T) *SoftwareBackend {
	t.Helper()
	b, err := NewSoftwareBackend(newMemStore(), fastHashConfig())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestProvision_SecondInitVerifiesInsteadOfColliding is the reason Provision
// exists: barrier-init and ceremony-init both supply the PINs, and the second
// one must verify them rather than fail with ErrPINAlreadySet.
func TestProvision_SecondInitVerifiesInsteadOfColliding(t *testing.T) {
	b := newProvisionBackend(t)
	if err := Provision(context.Background(), b, "so-pin-123", "user-pin-123"); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if err := Provision(context.Background(), b, "so-pin-123", "user-pin-123"); err != nil {
		t.Fatalf("second provision with the same PINs: %v", err)
	}
	if err := b.VerifySOPIN("so-pin-123"); err != nil {
		t.Fatalf("SO PIN not set: %v", err)
	}
	if err := b.VerifyUserPIN("user-pin-123"); err != nil {
		t.Fatalf("user PIN not set: %v", err)
	}
}

// TestRequireProvisionedSOPIN_RefusesUntilProvisioned pins the gate rekey
// start and holder revocation use: unlike RequireSOPIN it refuses while no SO
// PIN has been provisioned, and it fails closed without a backend.
func TestRequireProvisionedSOPIN_RefusesUntilProvisioned(t *testing.T) {
	if err := RequireProvisionedSOPIN(context.Background(), nil, "so-pin-123"); !errors.Is(err, ErrBackendNotConfigured) {
		t.Fatalf("no backend = %v, want ErrBackendNotConfigured", err)
	}
	b := newProvisionBackend(t)
	if err := RequireProvisionedSOPIN(context.Background(), b, "so-pin-123"); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("not provisioned = %v, want ErrPINNotSet", err)
	}
	if err := RequireSOPIN(context.Background(), b, "so-pin-123"); err != nil {
		t.Fatalf("RequireSOPIN still admits an unprovisioned backend: %v", err)
	}
	if err := Provision(context.Background(), b, "so-pin-123", ""); err != nil {
		t.Fatal(err)
	}
	if err := RequireProvisionedSOPIN(context.Background(), b, "so-pin-999"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("wrong PIN = %v, want ErrPINInvalid", err)
	}
	if err := RequireProvisionedSOPIN(context.Background(), b, "so-pin-123"); err != nil {
		t.Fatalf("right PIN = %v", err)
	}
}

// TestProvision_NeverOverwritesAPIN refuses a second provision with a
// different PIN instead of replacing it.
func TestProvision_NeverOverwritesAPIN(t *testing.T) {
	b := newProvisionBackend(t)
	if err := Provision(context.Background(), b, "so-pin-123", "user-pin-123"); err != nil {
		t.Fatal(err)
	}
	if err := Provision(context.Background(), b, "so-pin-999", ""); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("different SO PIN = %v, want ErrPINInvalid", err)
	}
	if err := Provision(context.Background(), b, "so-pin-123", "user-pin-999"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("different user PIN = %v, want ErrPINInvalid", err)
	}
	if err := b.VerifySOPIN("so-pin-123"); err != nil {
		t.Fatal("the original SO PIN was replaced")
	}
}

// TestProvision_FailsClosedWithoutABackend pins the no-fallback rule.
func TestProvision_FailsClosedWithoutABackend(t *testing.T) {
	if err := Provision(context.Background(), nil, "so-pin-123", "user-pin-123"); !errors.Is(err, ErrBackendNotConfigured) {
		t.Fatalf("Provision(context.Background(), nil) = %v", err)
	}
	if err := RequireSOPIN(context.Background(), nil, "so-pin-123"); !errors.Is(err, ErrBackendNotConfigured) {
		t.Fatalf("RequireSOPIN(context.Background(), nil) = %v", err)
	}
	if err := Provision(context.Background(), nil, "", ""); err != nil {
		t.Fatalf("nothing to provision = %v", err)
	}
}

func TestProvision_UserPINNeedsSOPIN(t *testing.T) {
	b := newProvisionBackend(t)
	if err := Provision(context.Background(), b, "", "user-pin-123"); !errors.Is(err, ErrSOPINRequired) {
		t.Fatalf("user PIN alone = %v", err)
	}
	if err := Provision(context.Background(), b, "so-pin-123", ""); err != nil {
		t.Fatal(err)
	}
	if b.UserPINSet() {
		t.Fatal("user PIN set without one being supplied")
	}
	if err := Provision(context.Background(), b, "short", ""); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("wrong SO PIN on a provisioned backend = %v", err)
	}
	c := newProvisionBackend(t)
	if err := Provision(context.Background(), c, "short", ""); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("too-short first SO PIN = %v", err)
	}
}

// TestProvision_SurfacesTheUserPINBackendError proves a user PIN the backend
// refuses fails Provision with the backend's own error, after the SO PIN was set.
func TestProvision_SurfacesTheUserPINBackendError(t *testing.T) {
	b := newProvisionBackend(t)
	if err := Provision(context.Background(), b, "so-pin-123", "short"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("too-short user PIN = %v, want ErrPINTooShort", err)
	}
	if !b.SOPINSet() {
		t.Fatal("SO PIN not set before the user PIN was refused")
	}
	if b.UserPINSet() {
		t.Fatal("a refused user PIN was set")
	}
}

func TestRequireSOPIN_GatesOnTheProvisionedPIN(t *testing.T) {
	b := newProvisionBackend(t)
	if err := RequireSOPIN(context.Background(), b, "anything"); err != nil {
		t.Fatalf("no PIN provisioned yet = %v", err)
	}
	if err := Provision(context.Background(), b, "so-pin-123", ""); err != nil {
		t.Fatal(err)
	}
	if err := RequireSOPIN(context.Background(), b, "so-pin-123"); err != nil {
		t.Fatalf("right PIN = %v", err)
	}
	if err := RequireSOPIN(context.Background(), b, "so-pin-124"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("wrong PIN = %v", err)
	}
}
