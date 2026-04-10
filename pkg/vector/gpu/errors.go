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
	"errors"
	"strconv"
)

// Sentinel errors for GPU operations.
var (
	// ErrGPUNotCompiled indicates GPU support was not compiled into the binary.
	ErrGPUNotCompiled = errors.New("GPU support not compiled (build with -tags gpu)")

	// ErrDeviceNotFound indicates the requested GPU device does not exist.
	ErrDeviceNotFound = errors.New("GPU device not found")

	// ErrOutOfMemory indicates GPU memory has been exhausted.
	ErrOutOfMemory = errors.New("GPU out of memory")

	// ErrKernelFailed indicates a GPU kernel execution failure.
	ErrKernelFailed = errors.New("GPU kernel execution failed")

	// ErrNotInitialized indicates the GPU has not been initialized.
	ErrNotInitialized = errors.New("GPU not initialized")
)

// GPUDeviceError represents an error related to a specific GPU device.
type GPUDeviceError struct {
	DeviceID int
	Message  string
	Err      error
}

// Error implements the error interface.
func (e *GPUDeviceError) Error() string {
	return "GPU device " + strconv.Itoa(e.DeviceID) + ": " + e.Message
}

// Unwrap returns the underlying error for errors.Is and errors.As support.
func (e *GPUDeviceError) Unwrap() error {
	return e.Err
}

// GPUMemoryError represents a GPU memory allocation failure.
type GPUMemoryError struct {
	RequestedBytes int64
	AvailableBytes int64
}

// Error implements the error interface.
func (e *GPUMemoryError) Error() string {
	return "GPU memory allocation failed: requested " +
		strconv.FormatInt(e.RequestedBytes, 10) +
		" bytes, available " +
		strconv.FormatInt(e.AvailableBytes, 10) + " bytes"
}

// GPUKernelError represents a GPU kernel execution failure.
type GPUKernelError struct {
	Kernel string
	Err    error
}

// Error implements the error interface.
func (e *GPUKernelError) Error() string {
	return "GPU kernel " + e.Kernel + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is and errors.As support.
func (e *GPUKernelError) Unwrap() error {
	return e.Err
}
