// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

func TestVectorServiceClosedError(t *testing.T) {
	var err error = &VectorServiceClosedError{}
	assert.Equal(t, "service: vector service is closed", err.Error())
	requireErrorAs[*VectorServiceClosedError](t, fmt.Errorf("wrapped: %w", err))
}

func TestVectorJobNotFoundError(t *testing.T) {
	assert.Equal(t, "service: vector rebuild job not found: job-1",
		(&VectorJobNotFoundError{Job: "rebuild", ID: "job-1"}).Error())
	assert.Equal(t, "service: vector compaction job not found: ns",
		(&VectorJobNotFoundError{Job: "compaction", ID: "ns"}).Error())
}

func TestVectorOperationError(t *testing.T) {
	cause := errors.New("disk full")
	err := &VectorOperationError{Op: "upsert vector", Err: cause}
	assert.Equal(t, "service: vector upsert vector failed: disk full", err.Error())
	assert.ErrorIs(t, err, cause, "Unwrap must expose the cause")

	bare := &VectorOperationError{Op: "raft proposal"}
	assert.Equal(t, "service: vector raft proposal failed", bare.Error())
	assert.NoError(t, bare.Unwrap())
}

// TestVectorStorageError_TranslatesEverySentinel pins go-qrdb#331's
// translation: each storage sentinel becomes the typed error the transports
// classify, wrapped or not, and names the LOGICAL namespace.
func TestVectorStorageError_TranslatesEverySentinel(t *testing.T) {
	for _, tc := range []struct {
		sentinel error
		check    func(t *testing.T, err error)
	}{
		{vector.ErrNamespaceNotFound, func(t *testing.T, err error) {
			assert.Equal(t, "docs", requireErrorAs[*NamespaceNotFoundError](t, err).Namespace)
		}},
		{vector.ErrNamespaceExists, func(t *testing.T, err error) {
			assert.Equal(t, "docs", requireErrorAs[*NamespaceExistsError](t, err).Namespace)
		}},
		{vector.ErrVectorNotFound, func(t *testing.T, err error) {
			got := requireErrorAs[*VectorNotFoundError](t, err)
			assert.Equal(t, "docs", got.Namespace)
			assert.Equal(t, "42", got.ID)
		}},
		{vector.ErrStorageClosed, func(t *testing.T, err error) {
			requireErrorAs[*VectorServiceClosedError](t, err)
		}},
		{vector.ErrInvalidNamespace, func(t *testing.T, err error) { requireErrorAs[*InvalidVectorError](t, err) }},
		{vector.ErrInvalidID, func(t *testing.T, err error) { requireErrorAs[*InvalidVectorError](t, err) }},
		{vector.ErrInvalidVector, func(t *testing.T, err error) { requireErrorAs[*InvalidVectorError](t, err) }},
		{vector.ErrEmptyBatch, func(t *testing.T, err error) { requireErrorAs[*InvalidVectorError](t, err) }},
		{vector.ErrBatchTooLarge, func(t *testing.T, err error) { requireErrorAs[*InvalidVectorError](t, err) }},
	} {
		t.Run(tc.sentinel.Error(), func(t *testing.T) {
			tc.check(t, vectorStorageError("op", "docs", 42, tc.sentinel))
			tc.check(t, vectorStorageError("op", "docs", 42, fmt.Errorf("pebble: %w", tc.sentinel)))
		})
	}
	if len(vectorStorageErrors) != 9 {
		t.Errorf("vectorStorageErrors has %d rows; this test covers 9 — add the new sentinel here", len(vectorStorageErrors))
	}
}

// TestVectorStorageError_ClosedStoreWinsOverWhatItWraps: an error carrying
// both a closed store and an absence is reported as the closed store — the
// ordered table's point, which a map could not guarantee.
func TestVectorStorageError_ClosedStoreWinsOverWhatItWraps(t *testing.T) {
	both := errors.Join(vector.ErrNamespaceNotFound, vector.ErrStorageClosed)
	for i := 0; i < 20; i++ {
		requireErrorAs[*VectorServiceClosedError](t, vectorStorageError("op", "docs", 0, both))
	}
}

// TestVectorStorageError_UnknownIsAServerFault: anything the storage layer
// does not name is the server's, not the caller's.
func TestVectorStorageError_UnknownIsAServerFault(t *testing.T) {
	cause := errors.New("io error")
	got := requireErrorAs[*VectorOperationError](t, vectorStorageError("upsert vector", "docs", 1, cause))
	assert.Equal(t, "upsert vector", got.Op)
	assert.ErrorIs(t, got, cause)
}
