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

func TestErrGPUNotCompiled_SentinelIdentity(t *testing.T) {
	// Verify sentinel errors are distinct.
	if errors.Is(ErrGPUNotCompiled, ErrDeviceNotFound) {
		t.Error("ErrGPUNotCompiled should not match ErrDeviceNotFound")
	}
	if errors.Is(ErrDeviceNotFound, ErrOutOfMemory) {
		t.Error("ErrDeviceNotFound should not match ErrOutOfMemory")
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

func TestGPUMemoryError_ZeroValues(t *testing.T) {
	err := &GPUMemoryError{
		RequestedBytes: 0,
		AvailableBytes: 0,
	}
	msg := err.Error()
	if !strings.Contains(msg, "GPU memory allocation failed") {
		t.Errorf("expected prefix in message, got: %s", msg)
	}
}
