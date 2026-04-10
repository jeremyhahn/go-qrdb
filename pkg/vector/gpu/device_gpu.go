//go:build qrdb_vector && gpu

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

import "sync/atomic"

// MemoryInfo contains GPU memory statistics reported by the CUDA runtime.
type MemoryInfo struct {
	// TotalBytes is the total GPU memory on the device in bytes.
	TotalBytes int64

	// UsedBytes is the currently used GPU memory in bytes.
	UsedBytes int64

	// AvailableBytes is the free GPU memory in bytes.
	AvailableBytes int64
}

// DeviceManager manages CUDA device lifecycle, initialization, and health checks.
// It is safe to call Initialize multiple times; subsequent calls are no-ops.
type DeviceManager struct {
	config      *GPUConfig
	initialized atomic.Bool
}

// NewDeviceManager creates a DeviceManager for the device specified in config.
// The GPU is not initialized until Initialize is called.
func NewDeviceManager(config *GPUConfig) (*DeviceManager, error) {
	return &DeviceManager{config: config}, nil
}

// Initialize sets up the CUDA device identified by config.DeviceID.
// It is idempotent: calling it more than once returns nil without
// reinitializing the device.
func (d *DeviceManager) Initialize() error {
	if d.initialized.Load() {
		return nil
	}
	if err := cudaInit(d.config.DeviceID); err != nil {
		return err
	}
	d.initialized.Store(true)
	return nil
}

// GetMemoryInfo queries the CUDA runtime for memory statistics.
// Returns ErrNotInitialized if Initialize has not been called.
func (d *DeviceManager) GetMemoryInfo() (*MemoryInfo, error) {
	if !d.initialized.Load() {
		return nil, ErrNotInitialized
	}
	total, used, available, err := cudaGetMemoryInfo()
	if err != nil {
		return nil, err
	}
	return &MemoryInfo{
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: available,
	}, nil
}

// HealthCheck verifies the GPU device is responsive by executing a lightweight
// CUDA operation. Returns ErrNotInitialized if Initialize has not been called.
func (d *DeviceManager) HealthCheck() error {
	if !d.initialized.Load() {
		return ErrNotInitialized
	}
	return cudaHealthCheck()
}

// Close releases all GPU resources. After Close, the DeviceManager must not be used.
func (d *DeviceManager) Close() error {
	if d.initialized.Load() {
		cudaCleanup()
		d.initialized.Store(false)
	}
	return nil
}
