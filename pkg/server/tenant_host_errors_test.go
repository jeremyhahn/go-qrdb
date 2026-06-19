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

package server

import (
	"errors"
	"testing"
)

func TestTenantHostError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("disk full")
	e := &TenantHostError{TenantID: "acme", Op: "build", Err: inner}

	const want = "server: tenant host build for tenant acme: disk full"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	// Unwrap exposes the cause for errors.Is/As.
	if !errors.Is(e, inner) {
		t.Errorf("errors.Is(e, inner) = false, want true")
	}
	if e.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", e.Unwrap(), inner)
	}
}

func TestTenantHostSentinelErrors(t *testing.T) {
	// The package-level sentinels carry stable, distinct messages.
	cases := []struct {
		err  error
		want string
	}{
		{ErrTenantHostUnavailable, "server: tenant host registry not configured"},
		{ErrTenantDataRootUnset, "server: tenant data root is unset"},
		{ErrTenantShardNoLeader, "server: tenant shard did not elect a leader"},
	}
	for _, c := range cases {
		if c.err.Error() != c.want {
			t.Errorf("sentinel = %q, want %q", c.err.Error(), c.want)
		}
	}
}
