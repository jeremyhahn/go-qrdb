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

package config

import (
	"errors"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestReplicaForwardingConfig_OnUnlessTurnedOff: unset is on; only an
// explicit false turns it off.
func TestReplicaForwardingConfig_OnUnlessTurnedOff(t *testing.T) {
	on, off := true, false
	for name, tc := range map[string]struct {
		enabled *bool
		want    bool
	}{"unset": {nil, true}, "true": {&on, true}, "false": {&off, false}} {
		if got := (ReplicaForwardingConfig{Enabled: tc.enabled}).On(); got != tc.want {
			t.Errorf("%s: On() = %v; want %v", name, got, tc.want)
		}
	}
}

// TestReplicaForwardingConfig_TimeoutDuration: unset is the default, a
// duration is itself, and a malformed, zero or negative one is refused -- by
// the accessor and by Validate alike.
func TestReplicaForwardingConfig_TimeoutDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"": DefaultReplicaForwardingTimeout, "3s": 3 * time.Second} {
		got, err := ReplicaForwardingConfig{Timeout: in}.TimeoutDuration()
		if err != nil || got != want {
			t.Errorf("TimeoutDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "0", "-1s"} {
		var ve *ValidationError
		if _, err := (ReplicaForwardingConfig{Timeout: bad}).TimeoutDuration(); !errors.As(err, &ve) || ve.Field != "replica_forwarding.timeout" {
			t.Errorf("TimeoutDuration(%q) err = %v; want a replica_forwarding.timeout ValidationError", bad, err)
		}
	}
	cfg := validConfig()
	cfg.ReplicaForwarding.Timeout = "0"
	var ve *ValidationError
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "replica_forwarding.timeout" {
		t.Fatalf("Validate with a zero timeout = %v", err)
	}
}

// TestReplicaForwardingConfig_YAML: the section's keys are the documented ones.
func TestReplicaForwardingConfig_YAML(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("replica_forwarding:\n  enabled: false\n  timeout: 4s\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ReplicaForwarding.On() || cfg.ReplicaForwarding.Timeout != "4s" {
		t.Fatalf("replica_forwarding = %+v; want off with a 4s timeout", cfg.ReplicaForwarding)
	}
}
