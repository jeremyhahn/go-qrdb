// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"errors"
	"strings"
	"testing"
)

func TestErrors_MessagesNameTheSubject(t *testing.T) {
	cause := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"ProviderAlreadyRegisteredError", &ProviderAlreadyRegisteredError{Provider: "dir"}, []string{"dir"}},
		{"ProviderNotRegisteredError", &ProviderNotRegisteredError{Provider: "luks2", Available: []string{"dir"}}, []string{"luks2", "dir"}},
		{"InvalidProviderError", &InvalidProviderError{Reason: "nil provider"}, []string{"nil provider"}},
		{"InvalidSpecError", &InvalidSpecError{Pool: "fast", Reason: "empty pool name"}, []string{"fast", "empty pool name"}},
		{"OpenError", &OpenError{Provider: "dir", Pool: "fast", Err: cause}, []string{"dir", "fast", "boom"}},
		{"UsageError", &UsageError{Pool: "fast", Err: cause}, []string{"fast", "boom"}},
		{"ClosedVolumeError", &ClosedVolumeError{Pool: "fast"}, []string{"fast"}},
		{"InvalidMasterKeyError", &InvalidMasterKeyError{}, []string{"master key"}},
		{"InvalidVolumeIdentityError", &InvalidVolumeIdentityError{NodeID: "node-1", Pool: "fast"}, []string{"node-1", "fast"}},
		{"VolumeKeyDerivationError", &VolumeKeyDerivationError{NodeID: "node-1", Pool: "fast", Err: cause}, []string{"node-1", "fast", "boom"}},
		{"notADirectoryError", &notADirectoryError{}, []string{"directory"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := c.err.Error()
			if msg == "" {
				t.Fatal("Error() returned an empty string")
			}
			for _, want := range c.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("Error() = %q, want it to contain %q", msg, want)
				}
			}
		})
	}
}

func TestErrors_UnwrapReachesCause(t *testing.T) {
	cause := errors.New("boom")

	cases := []struct {
		name string
		err  error
	}{
		{"OpenError", &OpenError{Provider: "dir", Pool: "fast", Err: cause}},
		{"UsageError", &UsageError{Pool: "fast", Err: cause}},
		{"VolumeKeyDerivationError", &VolumeKeyDerivationError{NodeID: "node-1", Pool: "fast", Err: cause}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !errors.Is(c.err, cause) {
				t.Fatalf("errors.Is(%v, cause) = false, want true", c.err)
			}
		})
	}
}
