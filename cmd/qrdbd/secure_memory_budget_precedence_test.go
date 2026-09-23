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

// Tests for --secure-memory-budget-mib / QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB
// / server.secure_memory_budget_mib precedence (go-qrdb#114 review, round 2:
// the setting had no test proving flag/env/file/default resolve in the
// documented order, following the same shape bind_host_test.go already pins
// for --host).
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestSecureMemoryBudget_DefaultsToDefaultsLayer proves the flag default is
// the defaults-layer value and not a second, independent literal, and that
// the value is zero: "not configured", so runStart derives the budget from
// the host instead of assuming 64 MiB fits under its RLIMIT_MEMLOCK.
func TestSecureMemoryBudget_DefaultsToDefaultsLayer(t *testing.T) {
	f := resolveWithArgs(t)
	assert.Equal(t, qrdbconfig.DefaultConfig().Server.SecureMemoryBudgetMiB, f.secureMemoryBudgetMiB)
	assert.Zero(t, f.secureMemoryBudgetMiB, "an unconfigured budget must reach runStart as 0 so it is derived")
}

// TestSecureMemoryBudget_FromConfigFile: server.secure_memory_budget_mib in
// YAML reaches the resolved flags.
func TestSecureMemoryBudget_FromConfigFile(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443
  secure_memory_budget_mib: 128`)
	f := resolveWithArgs(t, "--config", cfgPath)
	assert.Equal(t, int64(128), f.secureMemoryBudgetMiB)
}

// TestSecureMemoryBudget_FromEnv: QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB reaches
// the resolved flags and beats the file.
func TestSecureMemoryBudget_FromEnv(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443
  secure_memory_budget_mib: 128`)
	t.Setenv("QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB", "256")
	f := resolveWithArgs(t, "--config", cfgPath)
	assert.Equal(t, int64(256), f.secureMemoryBudgetMiB)
}

// TestSecureMemoryBudget_FlagBeatsEnvAndFile pins the documented precedence
// flags > env > file > defaults on this key.
func TestSecureMemoryBudget_FlagBeatsEnvAndFile(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443
  secure_memory_budget_mib: 128`)
	t.Setenv("QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB", "256")
	f := resolveWithArgs(t, "--config", cfgPath, "--secure-memory-budget-mib", "512")
	assert.Equal(t, int64(512), f.secureMemoryBudgetMiB)
}

// TestSecureMemoryBudget_InvalidValueFailsConfigValidation proves a negative
// configured value is refused at config load rather than reaching runStart,
// mirroring the other server.* range checks in pkg/config.
func TestSecureMemoryBudget_InvalidValueFailsConfigValidation(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443
  secure_memory_budget_mib: -1`)
	_, err := resolveStartFlagsErr(t, cfgPath)
	require.Error(t, err)
}

// resolveStartFlagsErr is resolveWithArgs's error-returning counterpart, for
// the one case here that must fail rather than resolve.
func resolveStartFlagsErr(t *testing.T, cfgPath string) (*startFlags, error) {
	t.Helper()
	cmd, f := newStartCmdWithFlags()
	if err := cmd.ParseFlags([]string{"--config", cfgPath}); err != nil {
		return nil, err
	}
	return f, resolveStartFlags(cmd, f)
}
