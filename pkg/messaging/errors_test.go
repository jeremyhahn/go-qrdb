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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DeltaNilInputError
// ---------------------------------------------------------------------------

func TestDeltaNilInputError_Error_IncludesParameter(t *testing.T) {
	t.Parallel()
	err := &DeltaNilInputError{Parameter: "new entity"}
	got := err.Error()
	assert.Contains(t, got, "new entity")
	assert.Contains(t, got, "delta computation requires non-nil")
}

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

func TestDeltaNilInputError_ImplementsErrorInterface(t *testing.T) {
	t.Parallel()
	var err error = &DeltaNilInputError{Parameter: "test"}
	require.NotEmpty(t, err.Error())
}

func TestDeltaNilInputError_ErrorsAs(t *testing.T) {
	t.Parallel()
	err := &DeltaNilInputError{Parameter: "new entity"}
	var target *DeltaNilInputError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "new entity", target.Parameter)
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

func TestDeltaTypeMismatchError_ImplementsErrorInterface(t *testing.T) {
	t.Parallel()
	var err error = &DeltaTypeMismatchError{OldType: "a", NewType: "b"}
	require.NotEmpty(t, err.Error())
}

func TestDeltaTypeMismatchError_ErrorsAs(t *testing.T) {
	t.Parallel()
	err := &DeltaTypeMismatchError{OldType: "string", NewType: "int", Path: "field"}
	var target *DeltaTypeMismatchError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "string", target.OldType)
	assert.Equal(t, "int", target.NewType)
	assert.Equal(t, "field", target.Path)
}

// ---------------------------------------------------------------------------
// DeltaMaxDepthExceededError
// ---------------------------------------------------------------------------

func TestDeltaMaxDepthExceededError_Error_IncludesPath(t *testing.T) {
	t.Parallel()
	err := &DeltaMaxDepthExceededError{MaxDepth: 10, Path: "a.b.c"}
	got := err.Error()
	assert.Contains(t, got, "max recursion depth exceeded")
	assert.Contains(t, got, "a.b.c")
}

func TestDeltaMaxDepthExceededError_Error_EmptyPath(t *testing.T) {
	t.Parallel()
	err := &DeltaMaxDepthExceededError{MaxDepth: 5, Path: ""}
	got := err.Error()
	assert.Equal(t, "max recursion depth exceeded at ", got)
}

func TestDeltaMaxDepthExceededError_ImplementsErrorInterface(t *testing.T) {
	t.Parallel()
	var err error = &DeltaMaxDepthExceededError{MaxDepth: 3, Path: "root"}
	require.NotEmpty(t, err.Error())
}

func TestDeltaMaxDepthExceededError_ErrorsAs(t *testing.T) {
	t.Parallel()
	err := &DeltaMaxDepthExceededError{MaxDepth: 10, Path: "deep.nested"}
	var target *DeltaMaxDepthExceededError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 10, target.MaxDepth)
	assert.Equal(t, "deep.nested", target.Path)
}

// ---------------------------------------------------------------------------
// DeltaDeserializationError
// ---------------------------------------------------------------------------

func TestDeltaDeserializationError_Error_IncludesCause(t *testing.T) {
	t.Parallel()
	err := &DeltaDeserializationError{Cause: "invalid json"}
	got := err.Error()
	assert.Contains(t, got, "failed to deserialize delta input")
	assert.Contains(t, got, "invalid json")
}

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

func TestDeltaDeserializationError_ImplementsErrorInterface(t *testing.T) {
	t.Parallel()
	var err error = &DeltaDeserializationError{Cause: "test"}
	require.NotEmpty(t, err.Error())
}

func TestDeltaDeserializationError_ErrorsAs(t *testing.T) {
	t.Parallel()
	err := &DeltaDeserializationError{Cause: "bad data"}
	var target *DeltaDeserializationError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "bad data", target.Cause)
}

// ---------------------------------------------------------------------------
// DeltaDiffError
// ---------------------------------------------------------------------------

func TestDeltaDiffError_Error_IncludesCause(t *testing.T) {
	t.Parallel()
	err := &DeltaDiffError{Cause: "comparison failed"}
	got := err.Error()
	assert.Contains(t, got, "diffable interface failed")
	assert.Contains(t, got, "comparison failed")
}

func TestDeltaDiffError_Error_FormatsCorrectly(t *testing.T) {
	t.Parallel()
	err := &DeltaDiffError{Cause: "version conflict"}
	assert.Equal(t, "diffable interface failed: version conflict", err.Error())
}

func TestDeltaDiffError_ImplementsErrorInterface(t *testing.T) {
	t.Parallel()
	var err error = &DeltaDiffError{Cause: "test"}
	require.NotEmpty(t, err.Error())
}

func TestDeltaDiffError_ErrorsAs(t *testing.T) {
	t.Parallel()
	err := &DeltaDiffError{Cause: "internal"}
	var target *DeltaDiffError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "internal", target.Cause)
}
