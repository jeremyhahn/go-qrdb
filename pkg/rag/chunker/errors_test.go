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

package chunker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmptyTextError_Error(t *testing.T) {
	e := &EmptyTextError{}
	assert.Contains(t, e.Error(), "empty")
}

func TestInvalidConfigError_Error(t *testing.T) {
	e := &InvalidConfigError{Reason: "chunk_size must be positive"}
	assert.Contains(t, e.Error(), "chunk_size must be positive")
}
