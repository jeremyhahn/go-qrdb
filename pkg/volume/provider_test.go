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

func TestSpec_FieldsRoundTrip(t *testing.T) {
	spec := Spec{
		Pool:    "fast",
		NodeID:  "node-1",
		Path:    "/mnt/fast",
		Options: map[string]string{"device": "/dev/nvme1n1"},
	}
	if spec.Pool != "fast" || spec.NodeID != "node-1" || spec.Path != "/mnt/fast" {
		t.Fatalf("unexpected Spec: %+v", spec)
	}
	if spec.Options["device"] != "/dev/nvme1n1" {
		t.Fatalf("unexpected Spec.Options: %+v", spec.Options)
	}
}

func TestUsage_ZeroValueIsUsable(t *testing.T) {
	var u Usage
	if u.SizeBytes != 0 || u.FreeBytes != 0 {
		t.Fatalf("zero Usage should be all zeros, got %+v", u)
	}
}
