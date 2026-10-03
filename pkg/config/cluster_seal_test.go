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
)

func TestSealConfig_ClusterSealTimingDefaults(t *testing.T) {
	timing, err := SealConfig{}.ClusterSealTiming()
	if err != nil {
		t.Fatal(err)
	}
	if timing.Lease != 15*time.Second || timing.PollInterval != time.Second || timing.AckTimeout != 5*time.Second {
		t.Fatalf("defaults = %+v", timing)
	}
	timing, err = SealConfig{ClusterLease: "30s", ClusterPollInterval: "2s", ClusterAckTimeout: "10s"}.ClusterSealTiming()
	if err != nil {
		t.Fatal(err)
	}
	if timing.Lease != 30*time.Second || timing.PollInterval != 2*time.Second || timing.AckTimeout != 10*time.Second {
		t.Fatalf("overrides = %+v", timing)
	}
}

func TestValidate_ClusterSealTiming(t *testing.T) {
	cases := []struct {
		name  string
		seal  SealConfig
		field string
	}{
		{"lease not a duration", SealConfig{ClusterLease: "soon"}, "seal.cluster_lease"},
		{"lease zero", SealConfig{ClusterLease: "0s"}, "seal.cluster_lease"},
		{"poll negative", SealConfig{ClusterPollInterval: "-1s"}, "seal.cluster_poll_interval"},
		{"poll not a duration", SealConfig{ClusterPollInterval: "x"}, "seal.cluster_poll_interval"},
		{"ack zero", SealConfig{ClusterAckTimeout: "0s"}, "seal.cluster_ack_timeout"},
		{"ack not a duration", SealConfig{ClusterAckTimeout: "x"}, "seal.cluster_ack_timeout"},
		{"poll too slow for the lease", SealConfig{ClusterLease: "3s", ClusterPollInterval: "2s"}, "seal.cluster_poll_interval"},
		// A cut-off node serves until its lease runs out, so the lease bounds
		// how long it can serve after it loses the records.
		{"lease over the cap", SealConfig{ClusterLease: "61s"}, "seal.cluster_lease"},
		// A node-barrier seal waits up to the ack timeout before it wipes
		// keys; a timeout past half the lease outlasts a peer's cut-off.
		{"ack over half the lease", SealConfig{ClusterLease: "20s", ClusterAckTimeout: "11s"}, "seal.cluster_ack_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.seal.Enabled = true
			cfg.Seal = tc.seal
			err := cfg.Validate()
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("Validate = %v, want a ValidationError on %s", err, tc.field)
			}
		})
	}
	for _, ok := range []SealConfig{
		{ClusterLease: "20s", ClusterPollInterval: "1s", ClusterAckTimeout: "5s"},
		{ClusterLease: "60s", ClusterPollInterval: "20s", ClusterAckTimeout: "30s"},
	} {
		cfg := validConfig()
		ok.Enabled = true
		cfg.Seal = ok
		if err := cfg.Validate(); err != nil {
			t.Fatalf("a valid cluster seal timing %+v was refused: %v", ok, err)
		}
	}
}
