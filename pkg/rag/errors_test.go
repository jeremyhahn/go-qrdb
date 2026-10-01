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

package rag

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDocumentTooLargeError_Error(t *testing.T) {
	e := &DocumentTooLargeError{Size: 1024, MaxSize: 512}
	msg := e.Error()
	assert.Contains(t, msg, "1024")
	assert.Contains(t, msg, "512")
}

func TestChunkingError_Error(t *testing.T) {
	cause := errors.New("split failed")
	e := &ChunkingError{Cause: cause}
	assert.Contains(t, e.Error(), "split failed")
	assert.Equal(t, cause, e.Unwrap())
}

func TestEmbeddingError_Error(t *testing.T) {
	cause := errors.New("provider down")
	e := &EmbeddingError{Cause: cause}
	assert.Contains(t, e.Error(), "provider down")
	assert.Equal(t, cause, e.Unwrap())
}

func TestRetrievalError_Error(t *testing.T) {
	cause := errors.New("index unavailable")
	e := &RetrievalError{Cause: cause}
	assert.Contains(t, e.Error(), "index unavailable")
	assert.Equal(t, cause, e.Unwrap())
}

func TestEmptyQueryError_Error(t *testing.T) {
	e := &EmptyQueryError{}
	assert.Contains(t, e.Error(), "empty")
}

func TestInvalidNamespaceError_Error(t *testing.T) {
	e := &InvalidNamespaceError{Namespace: "bad ns"}
	assert.Contains(t, e.Error(), "bad ns")
}
