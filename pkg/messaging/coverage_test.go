//go:build !integration

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sliceIgnoreZeroEntity has a slice field for testing ignoreZero on
// non-DeepEqual but both-zero slices (nil vs empty).
type sliceIgnoreZeroEntity struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}

// TestCompareSlices_NilToEmpty_ReportsChange verifies that compareSlices
// reports a change when old is nil and new is a non-nil empty slice, since
// isZeroValue treats only nil slices as zero (not empty slices).
func TestCompareSlices_NilToEmpty_ReportsChange(t *testing.T) {
	t.Parallel()
	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(true))

	// nil → []string{}: old is zero (nil), new is NOT zero (non-nil empty slice).
	old := &sliceIgnoreZeroEntity{ID: "1", Tags: nil}
	newVal := &sliceIgnoreZeroEntity{ID: "1", Tags: []string{}}

	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	// A change should be reported because []string{} is not nil.
	found := false
	for _, c := range payload.Changes {
		if c.Path == "tags" {
			found = true
		}
	}
	assert.True(t, found, "expected a change for tags when old is nil and new is non-nil empty slice")
}

// TestCompareStructs_SpecialType_Delete verifies that a time.Time field going
// from a non-zero value to zero generates an OpDelete change, exercising the
// `if newZero && !oldZero { change.Operation = OpDelete }` branch inside
// compareStructs for special types.
func TestCompareStructs_SpecialType_Delete(t *testing.T) {
	t.Parallel()

	type timeEntity struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expires_at"`
	}

	dc := NewDeltaComputer(newTestLogger(t), WithDeltaIgnoreZeroChanges(false))

	old := &timeEntity{ID: "1", ExpiresAt: time.Now()}
	newVal := &timeEntity{ID: "1", ExpiresAt: time.Time{}} // zero time

	payload, err := dc.Compute(old, newVal)
	require.NoError(t, err)

	found := false
	for _, c := range payload.Changes {
		if c.Path == "expires_at" && c.Operation == OpDelete {
			found = true
		}
	}
	assert.True(t, found, "expected OpDelete for expires_at going to zero time")
}
