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
	"errors"
)

// Sentinel errors for GPU operations.
var (
	// ErrGPUNotCompiled indicates GPU support was not compiled into the binary.
	ErrGPUNotCompiled = errors.New("GPU support not compiled (build with -tags gpu)")
)
