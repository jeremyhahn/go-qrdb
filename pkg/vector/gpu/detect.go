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
	"os"
	"os/exec"
)

const nvidiaDevicePath = "/dev/nvidia0"

// GPUProbeResult holds the results of a GPU hardware probe.
type GPUProbeResult struct {
	// Available indicates whether a usable NVIDIA GPU was detected.
	Available bool

	// DevicePath is the path to the GPU device node, non-empty only when the device exists.
	DevicePath string

	// DriverFound indicates whether the nvidia-smi management binary was found in PATH.
	DriverFound bool

	// Reason is a human-readable explanation when the GPU is unavailable.
	Reason string
}

// GPUAvailable returns true when an NVIDIA GPU device node exists at /dev/nvidia0
// and the nvidia-smi management binary is present in PATH.
// Both conditions must be satisfied for true to be returned.
func GPUAvailable() bool {
	result, err := ProbeGPU()
	if err != nil {
		return false
	}
	return result.Available
}

// ProbeGPU performs a detailed probe of NVIDIA GPU availability and returns
// a GPUProbeResult describing what was found. It checks for the /dev/nvidia0
// device node via os.Stat and for nvidia-smi via exec.LookPath.
// ProbeGPU never returns a non-nil error; the error return is reserved for
// future extension and will always be nil.
func ProbeGPU() (*GPUProbeResult, error) {
	result := &GPUProbeResult{}

	_, devErr := os.Stat(nvidiaDevicePath)
	if devErr == nil {
		result.DevicePath = nvidiaDevicePath
	}

	_, pathErr := exec.LookPath("nvidia-smi")
	result.DriverFound = pathErr == nil

	devicePresent := result.DevicePath != ""

	switch {
	case devicePresent && result.DriverFound:
		result.Available = true
	case !devicePresent && !result.DriverFound:
		result.Reason = "GPU device node not found and nvidia-smi not in PATH"
	case !devicePresent:
		result.Reason = "GPU device node not found at " + nvidiaDevicePath
	default:
		result.Reason = "nvidia-smi not found in PATH"
	}

	return result, nil
}
