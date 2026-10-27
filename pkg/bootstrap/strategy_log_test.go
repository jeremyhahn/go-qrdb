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

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// completeRecord returns the attributes of the "system bootstrap complete"
// record in a JSON log.
func completeRecord(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if rec["msg"] == "bootstrap: system bootstrap complete" {
			return rec
		}
	}
	t.Fatalf("no system bootstrap complete record in:\n%s", logs.String())
	return nil
}

// TestBootstrapSystem_LogsStrategyInForce is the #671 finding: a barrier
// initialized under none, bootstrapped under a configuration naming
// passphrase, logged strategy=passphrase. The log names the strategy that
// sealed the barrier, and the configured one beside it when they differ.
func TestBootstrapSystem_LogsStrategyInForce(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	if err := reg.SystemBarrier().Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("pre-initialize none barrier: %v", err)
	}

	var logs bytes.Buffer
	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyPassphrase,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem: %v", err)
	}

	rec := completeRecord(t, &logs)
	if got := rec["strategy"]; got != string(StrategyNone) {
		t.Fatalf("strategy = %v, want %s, the strategy the barrier is sealed under", got, StrategyNone)
	}
	if got := rec["configured_strategy"]; got != string(StrategyPassphrase) {
		t.Fatalf("configured_strategy = %v, want %s", got, StrategyPassphrase)
	}
}

// TestBootstrapSystem_LogsStrategyFreshInit covers the fresh-init path: the
// barrier is initialized here, under the configured strategy, so the log
// names it and carries no configured_strategy.
func TestBootstrapSystem_LogsStrategyFreshInit(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())

	var logs bytes.Buffer
	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyNone,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem: %v", err)
	}
	if result.BarrierStrategy != StrategyNone {
		t.Fatalf("BarrierStrategy = %q, want %q", result.BarrierStrategy, StrategyNone)
	}

	rec := completeRecord(t, &logs)
	if got := rec["strategy"]; got != string(StrategyNone) {
		t.Fatalf("strategy = %v, want %s", got, StrategyNone)
	}
	if _, ok := rec["configured_strategy"]; ok {
		t.Fatalf("configured_strategy logged although it matches: %v", rec)
	}
}
