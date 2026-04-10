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

package watch

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubClosedError_Error_ReturnsExpectedMessage(t *testing.T) {
	err := &HubClosedError{}
	want := "watch hub is closed"
	if got := err.Error(); got != want {
		t.Fatalf("HubClosedError.Error() = %q, want %q", got, want)
	}
}

func TestHubClosedError_ImplementsErrorInterface(t *testing.T) {
	var err error = &HubClosedError{}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestHubClosedError_ErrorsAs(t *testing.T) {
	err := &HubClosedError{}
	var target *HubClosedError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *HubClosedError")
	}
}

func TestHubClosedError_NotNil(t *testing.T) {
	err := &HubClosedError{}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

// TestWatchCreateError_Error verifies the error message includes the cause.
func TestWatchCreateError_Error(t *testing.T) {
	t.Run("formats message with cause", func(t *testing.T) {
		cause := errors.New("connection refused")
		err := &WatchCreateError{Cause: cause}
		msg := err.Error()
		assert.Contains(t, msg, "failed to create watch")
		assert.Contains(t, msg, "connection refused")
	})

	t.Run("formats message with wrapped cause", func(t *testing.T) {
		inner := errors.New("timeout")
		cause := &WatchCreateError{Cause: inner}
		msg := cause.Error()
		assert.Contains(t, msg, "failed to create watch")
		assert.Contains(t, msg, "timeout")
	})
}

// TestWatchCreateError_Unwrap verifies the error chain supports errors.Is and errors.As.
func TestWatchCreateError_Unwrap(t *testing.T) {
	t.Run("unwrap returns cause", func(t *testing.T) {
		cause := errors.New("underlying issue")
		err := &WatchCreateError{Cause: cause}
		assert.Equal(t, cause, err.Unwrap())
	})

	t.Run("errors.Is matches cause", func(t *testing.T) {
		sentinel := errors.New("sentinel error")
		err := &WatchCreateError{Cause: sentinel}
		require.True(t, errors.Is(err, sentinel))
	})

	t.Run("errors.As extracts typed error", func(t *testing.T) {
		cause := errors.New("some failure")
		err := &WatchCreateError{Cause: cause}

		var watchErr *WatchCreateError
		require.True(t, errors.As(err, &watchErr))
		assert.Equal(t, cause, watchErr.Cause)
	})
}
