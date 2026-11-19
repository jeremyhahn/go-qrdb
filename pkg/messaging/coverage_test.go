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
