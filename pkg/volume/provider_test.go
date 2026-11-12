// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import "testing"

func TestState_String_KnownStates(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{StateReady, "ready"},
		{StateLocked, "locked"},
		{StateUnavailable, "unavailable"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.state.String(); got != c.want {
				t.Fatalf("State(%d).String() = %q, want %q", c.state, got, c.want)
			}
		})
	}
}

func TestState_String_Unknown(t *testing.T) {
	if got := State(99).String(); got != "unknown" {
		t.Fatalf("State(99).String() = %q, want %q", got, "unknown")
	}
}
