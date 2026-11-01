// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredentialMigrationConflictError_Message verifies the message names
// the credential and every part that differs.
func TestCredentialMigrationConflictError_Message(t *testing.T) {
	cases := map[string]struct {
		err  *CredentialMigrationConflictError
		want string
	}{
		"all": {
			err:  &CredentialMigrationConflictError{Name: "db", Value: true, Versions: true, Policy: true},
			want: `credentials: migration conflict for "db": local copy differs in value, versions, policy`,
		},
		"value only": {
			err:  &CredentialMigrationConflictError{Name: "db", Value: true},
			want: `credentials: migration conflict for "db": local copy differs in value`,
		},
		"policy only": {
			err:  &CredentialMigrationConflictError{Name: "api", Policy: true},
			want: `credentials: migration conflict for "api": local copy differs in policy`,
		},
		"none flagged": {
			err:  &CredentialMigrationConflictError{Name: "x"},
			want: `credentials: migration conflict for "x": local copy differs in record`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

// TestCredentialMigrationConflictError_Fields verifies Fields lists the
// differing parts in order and is empty when none is flagged.
func TestCredentialMigrationConflictError_Fields(t *testing.T) {
	e := &CredentialMigrationConflictError{Name: "db", Versions: true, Policy: true}
	assert.Equal(t, []string{"versions", "policy"}, e.Fields())
	assert.Empty(t, (&CredentialMigrationConflictError{Name: "db"}).Fields())
}

// TestCredentialMigrationConflictError_As verifies errors.As finds the
// conflict through a wrapping error.
func TestCredentialMigrationConflictError_As(t *testing.T) {
	inner := &CredentialMigrationConflictError{Name: "db", Value: true}
	wrapped := &OperationError{Op: "migrate", Cause: inner}

	var got *CredentialMigrationConflictError
	require.True(t, errors.As(wrapped, &got))
	assert.Same(t, inner, got)
	assert.Equal(t, "db", got.Name)

	assert.False(t, errors.As(ErrCredentialNotFound, &got))
}
