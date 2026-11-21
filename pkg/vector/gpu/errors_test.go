//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package gpu

import (
	"errors"
	"strings"
	"testing"
)

func TestErrGPUNotCompiled(t *testing.T) {
	if ErrGPUNotCompiled == nil {
		t.Fatal("ErrGPUNotCompiled should not be nil")
	}
	msg := ErrGPUNotCompiled.Error()
	if !strings.Contains(msg, "GPU support not compiled") {
		t.Errorf("unexpected error message: %s", msg)
	}
}

func TestGPUDeviceError(t *testing.T) {
	err := &GPUDeviceError{
		DeviceID: 2,
		Message:  "initialization failed",
		Err:      ErrDeviceNotFound,
	}

	msg := err.Error()
	if !strings.Contains(msg, "GPU device 2") {
		t.Errorf("expected device ID in message, got: %s", msg)
	}
	if !strings.Contains(msg, "initialization failed") {
		t.Errorf("expected message text, got: %s", msg)
	}

	if !errors.Is(err, ErrDeviceNotFound) {
		t.Error("GPUDeviceError should unwrap to ErrDeviceNotFound")
	}
}

func TestGPUDeviceError_NilUnwrap(t *testing.T) {
	err := &GPUDeviceError{
		DeviceID: 0,
		Message:  "test",
		Err:      nil,
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap should return nil when Err is nil")
	}
}

func TestGPUMemoryError(t *testing.T) {
	err := &GPUMemoryError{
		RequestedBytes: 1073741824,
		AvailableBytes: 536870912,
	}

	msg := err.Error()
	if !strings.Contains(msg, "1073741824") {
		t.Errorf("expected requested bytes in message, got: %s", msg)
	}
	if !strings.Contains(msg, "536870912") {
		t.Errorf("expected available bytes in message, got: %s", msg)
	}
}

func TestFactoryError_ErrorAndUnwrap(t *testing.T) {
	err := &FactoryError{Op: "gpu factory: create remote index", Cause: ErrDeviceNotFound}

	if got, want := err.Error(), "gpu factory: create remote index: "+ErrDeviceNotFound.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Error("FactoryError should unwrap to its Cause")
	}
}

func TestFactoryError_WrappedCauseMessage(t *testing.T) {
	inner := &GPUMemoryError{RequestedBytes: 2048, AvailableBytes: 1024}
	err := &FactoryError{Op: "create index", Cause: inner}

	msg := err.Error()
	if !strings.HasPrefix(msg, "create index: ") {
		t.Errorf("expected Op prefix, got: %s", msg)
	}
	if !strings.Contains(msg, "2048") {
		t.Errorf("expected cause message in Error(), got: %s", msg)
	}
	var memErr *GPUMemoryError
	if !errors.As(err, &memErr) || memErr != inner {
		t.Error("errors.As should recover the wrapped *GPUMemoryError")
	}
}
