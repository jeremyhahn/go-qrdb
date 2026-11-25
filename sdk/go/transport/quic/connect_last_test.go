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

package quic

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestLastConnectFailure_KeepsRefusalOverBoundCut pins the keep-or-replace
// decision Connect makes after each failed attempt: once a failure is held,
// an attempt the caller's bound cut short (deadline or cancel wrapped in the
// attempt's error, or the bound already spent) does not replace it, while a
// genuine failure does.
func TestLastConnectFailure_KeepsRefusalOverBoundCut(t *testing.T) {
	refusal := &transport.ConnectionError{Address: "127.0.0.1:1", Err: errors.New("refused")}
	other := &transport.ConnectionError{Address: "127.0.0.1:1", Err: errors.New("reset")}
	deadlineCut := &transport.ConnectionError{
		Address: "127.0.0.1:1",
		Err:     &dialError{Err: fmt.Errorf("dial: %w", context.DeadlineExceeded)},
	}
	cancelCut := &transport.ConnectionError{
		Address: "127.0.0.1:1",
		Err:     &dialError{Err: fmt.Errorf("probe: %w", context.Canceled)},
	}

	tests := []struct {
		name  string
		last  error
		err   error
		spent bool
		want  error
	}{
		{"first failure is held", nil, refusal, false, refusal},
		{"first failure is held even when cut", nil, deadlineCut, true, deadlineCut},
		{"deadline-wrapped attempt keeps refusal", refusal, deadlineCut, false, refusal},
		{"cancel-wrapped attempt keeps refusal", refusal, cancelCut, false, refusal},
		{"attempt after the bound is spent keeps refusal", refusal, other, true, refusal},
		{"genuine failure replaces the held one", refusal, other, false, other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastConnectFailure(tc.last, tc.err, tc.spent); got != tc.want {
				t.Fatalf("lastConnectFailure = %v, want %v", got, tc.want)
			}
		})
	}
}
