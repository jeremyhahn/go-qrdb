//go:build qrdb_vector && !gpu

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

// MemoryInfo contains GPU memory statistics.
type MemoryInfo struct {
	// TotalBytes is the total GPU memory in bytes.
	TotalBytes int64

	// UsedBytes is the used GPU memory in bytes.
	UsedBytes int64

	// AvailableBytes is the available GPU memory in bytes.
	AvailableBytes int64
}

// DeviceManager manages GPU device lifecycle and health.
type DeviceManager struct{}

// NewDeviceManager creates a new GPU device manager.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func NewDeviceManager(_ *GPUConfig) (*DeviceManager, error) {
	return nil, ErrGPUNotCompiled
}

// Initialize initializes the GPU device.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (d *DeviceManager) Initialize() error {
	return ErrGPUNotCompiled
}

// GetMemoryInfo returns GPU memory usage.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (d *DeviceManager) GetMemoryInfo() (*MemoryInfo, error) {
	return nil, ErrGPUNotCompiled
}

// HealthCheck verifies GPU device health.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (d *DeviceManager) HealthCheck() error {
	return ErrGPUNotCompiled
}

// Close releases GPU resources.
// Returns ErrGPUNotCompiled when GPU support is not compiled in.
func (d *DeviceManager) Close() error {
	return ErrGPUNotCompiled
}
