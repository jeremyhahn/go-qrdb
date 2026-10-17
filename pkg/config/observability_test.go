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
	"log/slog"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

// TestObservabilityConfig_SlowSyncThresholdDefault: unset is one second.
func TestObservabilityConfig_SlowSyncThresholdDefault(t *testing.T) {
	if DefaultSlowSyncThreshold != time.Second {
		t.Fatalf("DefaultSlowSyncThreshold = %v; want 1s", DefaultSlowSyncThreshold)
	}
	got, err := ObservabilityConfig{}.SlowSyncThresholdDuration()
	if err != nil || got != time.Second {
		t.Fatalf("SlowSyncThresholdDuration() unset = %v, %v; want 1s", got, err)
	}
	got, err = DefaultConfig().Observability.SlowSyncThresholdDuration()
	if err != nil || got != time.Second {
		t.Fatalf("DefaultConfig() threshold = %v, %v; want 1s", got, err)
	}
}

// TestObservabilityConfig_SlowSyncThresholdBounds: a duration in (0, 1m] is
// itself; zero, a negative one, one above a minute and a malformed one are
// refused with the typed ValidationError naming the key.
func TestObservabilityConfig_SlowSyncThresholdBounds(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"50ms":  50 * time.Millisecond,
		"1ns":   time.Nanosecond,
		"1m":    time.Minute,
		"59.9s": 59900 * time.Millisecond,
	} {
		got, err := ObservabilityConfig{SlowSyncThreshold: in}.SlowSyncThresholdDuration()
		if err != nil || got != want {
			t.Errorf("SlowSyncThresholdDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"0", "0s", "-1s", "-1ns", "1m1s", "1m0.000000001s", "2h", "slow"} {
		got, err := ObservabilityConfig{SlowSyncThreshold: bad}.SlowSyncThresholdDuration()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != "observability.slow_sync_threshold" {
			t.Errorf("SlowSyncThresholdDuration(%q) = %v, %v; want an observability.slow_sync_threshold ValidationError", bad, got, err)
		}
	}
}

// TestObservabilityConfig_ValidateBounds: Validate refuses an out-of-range
// threshold and accepts the bound itself.
func TestObservabilityConfig_ValidateBounds(t *testing.T) {
	for _, bad := range []string{"0s", "-1s", "1m1s"} {
		cfg := validConfig()
		cfg.Observability.SlowSyncThreshold = bad
		var ve *ValidationError
		if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "observability.slow_sync_threshold" {
			t.Errorf("Validate with slow_sync_threshold %q = %v; want an observability.slow_sync_threshold ValidationError", bad, err)
		}
	}
	for _, good := range []string{"", "1m", "50ms"} {
		cfg := validConfig()
		cfg.Observability.SlowSyncThreshold = good
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate with slow_sync_threshold %q = %v", good, err)
		}
	}
}

// TestObservabilityConfig_ApplySlowSyncThreshold: a configured 50ms
// threshold becomes the process's, so the observers every storage backend
// and Raft WAL build warn on a 100ms sync and stay quiet on a 10ms one; an
// out-of-range threshold is refused and leaves the process's alone.
func TestObservabilityConfig_ApplySlowSyncThreshold(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	keepSlowSyncThreshold(t)
	log := syncwatchtest.NewLog()
	prev := slog.Default()
	slog.SetDefault(log.Logger())
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := (ObservabilityConfig{SlowSyncThreshold: "50ms"}).ApplySlowSyncThreshold(); err != nil {
		t.Fatalf("ApplySlowSyncThreshold(50ms) = %v", err)
	}
	if got := syncwatch.SlowThreshold(); got != 50*time.Millisecond {
		t.Fatalf("process threshold = %v; want 50ms", got)
	}
	for _, o := range []*syncwatch.Observer{
		syncwatch.ForStorage(syncwatch.BackendPebble),
		syncwatch.ForStorage(syncwatch.BackendFile),
		syncwatch.ForWAL(),
	} {
		o.Observe(10 * time.Millisecond)
		o.Observe(100 * time.Millisecond)
	}
	recs := log.Records()
	if len(recs) != 3 {
		t.Fatalf("got %d warnings; want one per 100ms sync and none for a 10ms one", len(recs))
	}
	for _, r := range recs {
		if r.Level != slog.LevelWarn || r.Attrs["threshold"] != 50*time.Millisecond || r.Attrs["took"] != 100*time.Millisecond {
			t.Fatalf("warning = %v %q %v; want WARN at threshold 50ms took 100ms", r.Level, r.Message, r.Attrs)
		}
	}

	for _, bad := range []string{"0s", "-1s", "1m1s"} {
		var ve *ValidationError
		if err := (ObservabilityConfig{SlowSyncThreshold: bad}).ApplySlowSyncThreshold(); !errors.As(err, &ve) || ve.Field != "observability.slow_sync_threshold" {
			t.Errorf("ApplySlowSyncThreshold(%q) = %v; want an observability.slow_sync_threshold ValidationError", bad, err)
		}
		if got := syncwatch.SlowThreshold(); got != 50*time.Millisecond {
			t.Errorf("after refusing %q the process threshold = %v; want 50ms kept", bad, got)
		}
	}

	if err := (ObservabilityConfig{}).ApplySlowSyncThreshold(); err != nil || syncwatch.SlowThreshold() != DefaultSlowSyncThreshold {
		t.Fatalf("ApplySlowSyncThreshold(unset) = %v, threshold %v; want the 1s default", err, syncwatch.SlowThreshold())
	}
}

// keepSlowSyncThreshold restores the process's slow-sync threshold when t
// ends.
func keepSlowSyncThreshold(t *testing.T) {
	t.Helper()
	prev := syncwatch.SlowThreshold()
	t.Cleanup(func() { syncwatch.SetSlowThreshold(prev) })
}

// TestObservabilityConfig_YAML: the key is observability.slow_sync_threshold.
func TestObservabilityConfig_YAML(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("observability:\n  slow_sync_threshold: 250ms\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.Observability.SlowSyncThresholdDuration(); err != nil || got != 250*time.Millisecond {
		t.Fatalf("observability = %+v (%v, %v); want a 250ms threshold", cfg.Observability, got, err)
	}
}

// TestObservabilityConfig_Load: Load reads the key from a file, refuses an
// out-of-range one, and takes the environment override.
func TestObservabilityConfig_Load(t *testing.T) {
	cfg, err := Load(writeMinimalConfig(t, "observability:\n  slow_sync_threshold: 1m\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := cfg.Observability.SlowSyncThresholdDuration(); got != time.Minute {
		t.Fatalf("loaded threshold = %v; want 1m", got)
	}

	var ve *ValidationError
	if _, err := Load(writeMinimalConfig(t, "observability:\n  slow_sync_threshold: 1m1s\n")); !errors.As(err, &ve) || ve.Field != "observability.slow_sync_threshold" {
		t.Fatalf("Load with 1m1s = %v; want an observability.slow_sync_threshold ValidationError", err)
	}

	t.Setenv("QRDB_OBSERVABILITY_SLOW_SYNC_THRESHOLD", "75ms")
	cfg, err = Load(writeMinimalConfig(t, "logging:\n  level: info\n"))
	if err != nil {
		t.Fatalf("Load with env override: %v", err)
	}
	if got, _ := cfg.Observability.SlowSyncThresholdDuration(); got != 75*time.Millisecond {
		t.Fatalf("env-overridden threshold = %v; want 75ms", got)
	}
}
