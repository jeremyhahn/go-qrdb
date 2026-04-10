//go:build qrdb_vector && !gpu

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
	"testing"
)

func TestNewDeviceManager_ReturnsStubError(t *testing.T) {
	dm, err := NewDeviceManager(DefaultGPUConfig())
	if dm != nil {
		t.Error("expected nil DeviceManager from stub")
	}
	if !errors.Is(err, ErrGPUNotCompiled) {
		t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
	}
}

func TestDeviceManager_AllMethodsReturnStubError(t *testing.T) {
	dm := &DeviceManager{}

	tests := []struct {
		name string
		fn   func() error
	}{
		{"Initialize", dm.Initialize},
		{"HealthCheck", dm.HealthCheck},
		{"Close", dm.Close},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if !errors.Is(err, ErrGPUNotCompiled) {
				t.Errorf("%s: expected ErrGPUNotCompiled, got: %v", tt.name, err)
			}
		})
	}
}

func TestDeviceManager_GetMemoryInfo_ReturnsStubError(t *testing.T) {
	dm := &DeviceManager{}
	info, err := dm.GetMemoryInfo()
	if info != nil {
		t.Error("expected nil MemoryInfo from stub")
	}
	if !errors.Is(err, ErrGPUNotCompiled) {
		t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
	}
}
