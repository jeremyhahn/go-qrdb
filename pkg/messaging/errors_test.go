//go:build !integration

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

package messaging

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// DeltaNilInputError
// ---------------------------------------------------------------------------

func TestDeltaNilInputError_Error_DifferentParameters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		param string
		want  string
	}{
		{
			name:  "new entity parameter",
			param: "new entity",
			want:  "delta computation requires non-nil new entity",
		},
		{
			name:  "new data parameter",
			param: "new data",
			want:  "delta computation requires non-nil new data",
		},
		{
			name:  "entity type parameter",
			param: "entity type",
			want:  "delta computation requires non-nil entity type",
		},
		{
			name:  "nil pointer parameter",
			param: "new entity (nil pointer)",
			want:  "delta computation requires non-nil new entity (nil pointer)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &DeltaNilInputError{Parameter: tt.param}
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

// ---------------------------------------------------------------------------
// DeltaTypeMismatchError
// ---------------------------------------------------------------------------

func TestDeltaTypeMismatchError_Error_WithoutPath(t *testing.T) {
	t.Parallel()
	err := &DeltaTypeMismatchError{
		OldType: "string",
		NewType: "int",
	}
	got := err.Error()
	assert.Equal(t, "type mismatch: string vs int", got)
}

func TestDeltaTypeMismatchError_Error_WithPath(t *testing.T) {
	t.Parallel()
	err := &DeltaTypeMismatchError{
		OldType: "non-pointer",
		NewType: "pointer",
		Path:    "entityType",
	}
	got := err.Error()
	assert.Equal(t, "type mismatch at entityType: non-pointer vs pointer", got)
}

// ---------------------------------------------------------------------------
// DeltaMaxDepthExceededError
// ---------------------------------------------------------------------------

func TestDeltaMaxDepthExceededError_Error_IncludesPath(t *testing.T) {
	t.Parallel()
	err := &DeltaMaxDepthExceededError{MaxDepth: 10, Path: "a.b.c"}
	assert.Equal(t, "max recursion depth exceeded at a.b.c", err.Error())
}

// ---------------------------------------------------------------------------
// DeltaDeserializationError
// ---------------------------------------------------------------------------

func TestDeltaDeserializationError_Error_DifferentCauses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause string
		want  string
	}{
		{
			name:  "old data cause",
			cause: "old data: unexpected end of JSON input",
			want:  "failed to deserialize delta input: old data: unexpected end of JSON input",
		},
		{
			name:  "new data cause",
			cause: "new data: invalid character 'x'",
			want:  "failed to deserialize delta input: new data: invalid character 'x'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &DeltaDeserializationError{Cause: tt.cause}
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

// ---------------------------------------------------------------------------
// DeltaDiffError
// ---------------------------------------------------------------------------

func TestDeltaDiffError_Error_FormatsCorrectly(t *testing.T) {
	t.Parallel()
	err := &DeltaDiffError{Cause: "version conflict"}
	assert.Equal(t, "diffable interface failed: version conflict", err.Error())
}
