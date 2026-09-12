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

//go:build !qrdb_vector

// Package gpu provides GPU-accelerated vector indexes. This stub version
// is compiled when vector support is disabled and exposes only the
// sentinel error identifying that GPU support is unavailable.
package gpu

import (
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// Sentinel errors for GPU operations.
var (
	// ErrGPUNotCompiled indicates GPU support was not compiled into the binary.
	ErrGPUNotCompiled error = &NotCompiledError{}
)

// NotCompiledError reports that GPU support was not built into this
// binary. It is a TYPE, not a bare errors.New sentinel, because the Raft Apply
// classifier reads the protocol.CapabilityError declaration below to tell a
// refusal from a fault: an untyped sentinel takes the fatal branch, halting
// the state machine that received it and costing the shard every subsequent
// linearizable read (go-qrdb#520).
type NotCompiledError struct{}

// Error returns the refusal, naming the build tag that enables the subsystem.
func (e *NotCompiledError) Error() string {
	return "GPU support not compiled (build with -tags gpu)"
}

// Capability names the absent subsystem, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *NotCompiledError) Capability() string {
	return "gpu"
}

// NotCompiledError is an absent capability, not a fault.
var _ protocol.CapabilityError = (*NotCompiledError)(nil)
