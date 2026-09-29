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
	if err := Provision(b, "so-pin-123", "user-pin-123"); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if err := Provision(b, "so-pin-123", "user-pin-123"); err != nil {
		t.Fatalf("second provision with the same PINs: %v", err)
	}
	if err := b.VerifySOPIN("so-pin-123"); err != nil {
		t.Fatalf("SO PIN not set: %v", err)
	}
	if err := b.VerifyUserPIN("user-pin-123"); err != nil {
		t.Fatalf("user PIN not set: %v", err)
	}
}

// TestProvision_NeverOverwritesAPIN refuses a second provision with a
// different PIN instead of replacing it.
func TestProvision_NeverOverwritesAPIN(t *testing.T) {
	b := newProvisionBackend(t)
	if err := Provision(b, "so-pin-123", "user-pin-123"); err != nil {
		t.Fatal(err)
	}
	if err := Provision(b, "so-pin-999", ""); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("different SO PIN = %v, want ErrPINInvalid", err)
	}
	if err := Provision(b, "so-pin-123", "user-pin-999"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("different user PIN = %v, want ErrPINInvalid", err)
	}
	if err := b.VerifySOPIN("so-pin-123"); err != nil {
		t.Fatal("the original SO PIN was replaced")
	}
}

// TestProvision_FailsClosedWithoutABackend pins the no-fallback rule.
func TestProvision_FailsClosedWithoutABackend(t *testing.T) {
	if err := Provision(nil, "so-pin-123", "user-pin-123"); !errors.Is(err, ErrBackendNotConfigured) {
		t.Fatalf("Provision(nil) = %v", err)
	}
	if err := RequireSOPIN(nil, "so-pin-123"); !errors.Is(err, ErrBackendNotConfigured) {
		t.Fatalf("RequireSOPIN(nil) = %v", err)
	}
	if err := Provision(nil, "", ""); err != nil {
		t.Fatalf("nothing to provision = %v", err)
	}
}

func TestProvision_UserPINNeedsSOPIN(t *testing.T) {
	b := newProvisionBackend(t)
	if err := Provision(b, "", "user-pin-123"); !errors.Is(err, ErrSOPINRequired) {
		t.Fatalf("user PIN alone = %v", err)
	}
	if err := Provision(b, "so-pin-123", ""); err != nil {
		t.Fatal(err)
	}
	if b.UserPINSet() {
		t.Fatal("user PIN set without one being supplied")
	}
	if err := Provision(b, "short", ""); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("wrong SO PIN on a provisioned backend = %v", err)
	}
	c := newProvisionBackend(t)
	if err := Provision(c, "short", ""); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("too-short first SO PIN = %v", err)
	}
}

func TestRequireSOPIN_GatesOnTheProvisionedPIN(t *testing.T) {
	b := newProvisionBackend(t)
	if err := RequireSOPIN(b, "anything"); err != nil {
		t.Fatalf("no PIN provisioned yet = %v", err)
	}
	if err := Provision(b, "so-pin-123", ""); err != nil {
		t.Fatal(err)
	}
	if err := RequireSOPIN(b, "so-pin-123"); err != nil {
		t.Fatalf("right PIN = %v", err)
	}
	if err := RequireSOPIN(b, "so-pin-124"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("wrong PIN = %v", err)
	}
}
