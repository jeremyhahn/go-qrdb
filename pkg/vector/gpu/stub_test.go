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

package gpu

import (
	"errors"
	"testing"
)

// TestStubErrGPUNotCompiled verifies the stub sentinel error matches the
// real package's error message so callers can rely on a single identity.
func TestStubErrGPUNotCompiled(t *testing.T) {
	if ErrGPUNotCompiled == nil {
		t.Fatal("ErrGPUNotCompiled is nil")
	}
	want := "GPU support not compiled (build with -tags gpu)"
	if ErrGPUNotCompiled.Error() != want {
		t.Errorf("ErrGPUNotCompiled = %q, want %q", ErrGPUNotCompiled.Error(), want)
	}
	if !errors.Is(ErrGPUNotCompiled, ErrGPUNotCompiled) {
		t.Error("ErrGPUNotCompiled does not match itself with errors.Is")
	}
}
