//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package gpu

import (
	"os"
	"os/exec"
	"testing"
)

// TestGPUAvailable_ReturnsBoolean verifies that GPUAvailable returns a bool
// without panicking, regardless of the underlying hardware.
func TestGPUAvailable_ReturnsBoolean(t *testing.T) {
	got := GPUAvailable()
	// The only valid outcomes are true or false; the call must not panic.
	if got != true && got != false {
		t.Errorf("GPUAvailable() returned an unexpected value: %v", got)
	}
}

// TestProbeGPU_ReturnsResult verifies that ProbeGPU returns a non-nil result
// with no error, and that result.Available is consistent with GPUAvailable().
func TestProbeGPU_ReturnsResult(t *testing.T) {
	result, err := ProbeGPU()
	if err != nil {
		t.Fatalf("ProbeGPU() returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("ProbeGPU() returned nil result")
	}
	if result.Available != GPUAvailable() {
		t.Errorf("ProbeGPU().Available = %v, want %v (GPUAvailable())", result.Available, GPUAvailable())
	}
}

// TestGPUProbeResult_ReasonConsistent verifies that the Reason field is
// populated when the GPU is unavailable and empty when it is available.
// Both branches are asserted so the test never needs to be skipped.
func TestGPUProbeResult_ReasonConsistent(t *testing.T) {
	result, err := ProbeGPU()
	if err != nil {
		t.Fatalf("ProbeGPU() returned unexpected error: %v", err)
	}
	if result.Available {
		// GPU present: Reason should be empty (no explanation needed).
		if result.Reason != "" {
			t.Errorf("ProbeGPU().Reason = %q when GPU is available; expected empty", result.Reason)
		}
	} else {
		// GPU absent: Reason must explain why.
		if result.Reason == "" {
			t.Error("ProbeGPU().Reason is empty when GPU is unavailable; expected a non-empty explanation")
		}
	}
}

// TestProbeGPU_FieldsConsistent verifies that DevicePath is non-empty only when
// the device node is present, and that DriverFound matches the exec.LookPath result.
func TestProbeGPU_FieldsConsistent(t *testing.T) {
	result, err := ProbeGPU()
	if err != nil {
		t.Fatalf("ProbeGPU() returned unexpected error: %v", err)
	}

	_, devErr := os.Stat(nvidiaDevicePath)
	deviceExists := devErr == nil

	if deviceExists && result.DevicePath == "" {
		t.Errorf("device node exists at %s but DevicePath is empty", nvidiaDevicePath)
	}
	if !deviceExists && result.DevicePath != "" {
		t.Errorf("device node absent but DevicePath = %q; expected empty string", result.DevicePath)
	}

	_, pathErr := exec.LookPath("nvidia-smi")
	driverInPath := pathErr == nil

	if result.DriverFound != driverInPath {
		t.Errorf("ProbeGPU().DriverFound = %v, want %v (exec.LookPath)", result.DriverFound, driverInPath)
	}
}
