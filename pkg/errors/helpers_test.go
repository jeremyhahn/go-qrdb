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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotImplemented(t *testing.T) {
	err := NotImplemented("feature.X", "not yet available")
	require.Error(t, err)

	var de *QRDBError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, ErrNotImplemented, de.Code)
	assert.Equal(t, "feature.X", de.Op)
	assert.Contains(t, err.Error(), "not yet available")
	assert.False(t, IsRetryable(err))
}

func TestCircuitOpen(t *testing.T) {
	err := CircuitOpen("downstream.Call", "payment-service")
	require.Error(t, err)

	var de *QRDBError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, ErrCircuitOpen, de.Code)
	assert.Equal(t, "downstream.Call", de.Op)
}

func TestIsCircuitOpen(t *testing.T) {
	assert.True(t, IsCircuitOpen(CircuitOpen("op", "my-service")))
	assert.False(t, IsCircuitOpen(nil))
	assert.False(t, IsCircuitOpen(New(ErrTimeout, "op", "timed out")))
	assert.False(t, IsCircuitOpen(errors.New("plain error")))
}
