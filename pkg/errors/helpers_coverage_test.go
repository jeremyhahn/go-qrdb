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

package errors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotFound_Helper(t *testing.T) {
	// Test with NotFound error
	err := NotFound("test.Op", "resource")
	assert.True(t, IsNotFound(err))

	// Test with nil
	assert.False(t, IsNotFound(nil))

	// Test with different error code
	err2 := New(ErrStorage, "test.Op", "storage error")
	assert.False(t, IsNotFound(err2))
}

func TestIsCanceled_Helper(t *testing.T) {
	// Test with Canceled error
	err := New(ErrCanceled, "test.Op", "canceled")
	assert.True(t, IsCanceled(err))

	// Test with nil
	assert.False(t, IsCanceled(nil))

	// Test with different error code
	err2 := New(ErrTimeout, "test.Op", "timeout")
	assert.False(t, IsCanceled(err2))
}

func TestIsInvalidInput_Helper(t *testing.T) {
	// Test with InvalidInput error
	err := InvalidInput("test.Op", "invalid input")
	assert.True(t, IsInvalidInput(err))

	// Test with nil
	assert.False(t, IsInvalidInput(nil))

	// Test with different error code
	err2 := New(ErrStorage, "test.Op", "storage error")
	assert.False(t, IsInvalidInput(err2))
}
