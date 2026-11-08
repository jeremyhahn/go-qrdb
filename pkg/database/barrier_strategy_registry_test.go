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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

const deviceMode = "fake-device"

// newDeviceBarrierManager returns a manager whose registry holds a fake device
// strategy under deviceMode, as go-xkms registers a hardware strategy.
func newDeviceBarrierManager(t *testing.T) (*DatabaseBarrierManager, *sealtest.DeviceStrategy) {
	t.Helper()
	device, err := sealtest.NewDeviceStrategy(deviceMode)
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	reg := seal.NewDefaultStrategyRegistry()
	if err := reg.Register(deviceMode, func() (seal.SealingStrategy, error) { return device, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mgr, err := NewDatabaseBarrierManager(testBarrierLogger(), newTestSystemBarrier(t), WithBarrierStrategyRegistry(reg))
	if err != nil {
		t.Fatalf("NewDatabaseBarrierManager: %v", err)
	}
	return mgr, device
}

// TestDatabaseBarrier_RegisteredDeviceStrategySealsTheBarrier: a database whose
// barrier mode names a registered strategy is sealed and unsealed by that
// device, with no passphrase.
func TestDatabaseBarrier_RegisteredDeviceStrategySealsTheBarrier(t *testing.T) {
	mgr, device := newDeviceBarrierManager(t)
	ctx := context.Background()
	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: deviceMode}

	if err := mgr.EnsureBarrier(ctx, "hwdb", "", cfg, seal.NewMemoryBackend(), seal.Credentials{}); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}
	if device.Wraps() == 0 {
		t.Fatal("the device never wrapped the root key")
	}
	status, err := mgr.BarrierStatus("hwdb", "")
	if err != nil || status.Sealed {
		t.Fatalf("BarrierStatus = %+v, %v; want an unsealed barrier", status, err)
	}

	if err := mgr.SealDatabase("hwdb", ""); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
	if err := mgr.UnsealDatabase(ctx, "hwdb", "", seal.Credentials{}); err != nil {
		t.Fatalf("UnsealDatabase: %v", err)
	}
	if device.Unwraps() == 0 {
		t.Fatal("the device never unwrapped the root key")
	}
}

// TestDatabaseBarrier_RegisterBarrierReopensUnderTheDevice: the re-open path a
// restarted node takes resolves the persisted mode through the registry too.
func TestDatabaseBarrier_RegisterBarrierReopensUnderTheDevice(t *testing.T) {
	mgr, device := newDeviceBarrierManager(t)
	ctx := context.Background()
	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: deviceMode}
	backend := seal.NewMemoryBackend()
	if err := mgr.EnsureBarrier(ctx, "hwdb", "", cfg, backend, seal.Credentials{}); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	reopened, err := NewDatabaseBarrierManager(testBarrierLogger(), newTestSystemBarrier(t), WithBarrierStrategyRegistry(mgr.strategies))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RegisterBarrier("hwdb", "", cfg, backend); err != nil {
		t.Fatalf("RegisterBarrier: %v", err)
	}
	if err := reopened.UnsealDatabase(ctx, "hwdb", "", seal.Credentials{}); err != nil {
		t.Fatalf("UnsealDatabase after re-open: %v", err)
	}
	if device.Unwraps() == 0 {
		t.Fatal("the re-opened barrier was not unsealed by the device")
	}
}

// TestDatabaseBarrier_UnregisteredModeIsRefusedEvenWithARegistry: a mode the
// registry does not hold is still refused, never silently downgraded to a
// passphrase barrier.
func TestDatabaseBarrier_UnregisteredModeIsRefusedEvenWithARegistry(t *testing.T) {
	mgr, _ := newDeviceBarrierManager(t)
	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "not-registered"}
	err := mgr.EnsureBarrier(context.Background(), "db", "", cfg, seal.NewMemoryBackend(), seal.Credentials{})
	var unsupported *UnsupportedBarrierStrategyError
	if !errors.As(err, &unsupported) || unsupported.Mode != "not-registered" {
		t.Fatalf("err = %v, want *UnsupportedBarrierStrategyError for not-registered", err)
	}
	if err := mgr.RegisterBarrier("db", "", cfg, seal.NewMemoryBackend()); !errors.As(err, &unsupported) {
		t.Fatalf("RegisterBarrier err = %v, want *UnsupportedBarrierStrategyError", err)
	}
}

// TestDatabaseBarrier_PassphraseStillWorksBesideARegistry: a registry that holds
// only a device strategy does not take the built-in passphrase mode away.
func TestDatabaseBarrier_PassphraseStillWorksBesideARegistry(t *testing.T) {
	mgr, _ := newDeviceBarrierManager(t)
	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase}
	if err := mgr.EnsureBarrier(context.Background(), "ppdb", "", cfg, seal.NewMemoryBackend(), testCreds()); err != nil {
		t.Fatalf("EnsureBarrier(passphrase): %v", err)
	}
}

func TestDatabaseBarrierManager_SupportsMode(t *testing.T) {
	plain := newTestBarrierManager(t)
	withDevice, _ := newDeviceBarrierManager(t)
	for _, tc := range []struct {
		mode        string
		plain, with bool
	}{
		{"", true, true},
		{BarrierModePassphrase, true, true},
		{BarrierModeShamir, true, true},
		{deviceMode, false, true},
		{"nope", false, false},
	} {
		if got := plain.SupportsMode(tc.mode); got != tc.plain {
			t.Errorf("no registry: SupportsMode(%q) = %v, want %v", tc.mode, got, tc.plain)
		}
		if got := withDevice.SupportsMode(tc.mode); got != tc.with {
			t.Errorf("with registry: SupportsMode(%q) = %v, want %v", tc.mode, got, tc.with)
		}
	}
}

// TestManager_BarrierModeSupported follows the barrier manager, and without one
// accepts only the built-in modes.
func TestManager_BarrierModeSupported(t *testing.T) {
	withDevice, _ := newDeviceBarrierManager(t)
	m := &Manager{barrierMgr: withDevice}
	if !m.barrierModeSupported(deviceMode) {
		t.Error("a registered mode is not supported with a barrier manager")
	}
	bare := &Manager{}
	if bare.barrierModeSupported(deviceMode) {
		t.Error("a registry-only mode is supported with no barrier manager")
	}
	if !bare.barrierModeSupported(BarrierModeShamir) || !bare.barrierModeSupported("") {
		t.Error("a built-in mode is unsupported with no barrier manager")
	}
}
