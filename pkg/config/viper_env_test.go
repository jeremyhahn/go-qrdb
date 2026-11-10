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
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeMinimalConfig writes a minimal valid YAML config and returns its path.
func writeMinimalConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoad_EnvOverridesFileValue verifies a QRDB_-prefixed env var overrides
// a value present in the YAML file (env has highest precedence).
func TestLoad_EnvOverridesFileValue(t *testing.T) {
	path := writeMinimalConfig(t, "logging:\n  level: info\n")

	t.Setenv("QRDB_LOGGING_LEVEL", "debug")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q (env override)", cfg.Logging.Level, "debug")
	}
}

// TestLoad_EnvOverridesAbsentFileKey verifies an env var overrides a nested
// key that the file does not mention at all (covered via registered defaults).
func TestLoad_EnvOverridesAbsentFileKey(t *testing.T) {
	// File omits the seal section entirely.
	path := writeMinimalConfig(t, "logging:\n  level: info\n")

	t.Setenv("QRDB_SEAL_STRATEGY", "shamir")
	t.Setenv("QRDB_SEAL_SHAMIR_THRESHOLD", "2")
	t.Setenv("QRDB_SEAL_SHAMIR_TOTAL", "3")
	t.Setenv("QRDB_SEAL_ENABLED", "true")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Seal.Strategy != "shamir" {
		t.Errorf("Seal.Strategy = %q, want %q (env override of absent file key)", cfg.Seal.Strategy, "shamir")
	}
	if cfg.Seal.ShamirTotal != 3 {
		t.Errorf("Seal.ShamirTotal = %d, want 3 (env override)", cfg.Seal.ShamirTotal)
	}
}

// TestLoad_FileOverridesDefault verifies the YAML file overrides a default
// when no env var is set.
func TestLoad_FileOverridesDefault(t *testing.T) {
	path := writeMinimalConfig(t, "logging:\n  level: warn\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("Logging.Level = %q, want %q (file override of default)", cfg.Logging.Level, "warn")
	}
}

// TestSealShareTTL_DefaultAndOverride covers the seal.share_ttl plumbing:
// the 15m default when unset, a YAML override, and a QRDB_SEAL_SHARE_TTL
// env override.
func TestSealShareTTL_DefaultAndOverride(t *testing.T) {
	// Default when the file omits seal.share_ttl.
	cfg, err := Load(writeMinimalConfig(t, "logging:\n  level: info\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err := cfg.Seal.ShareTTLDuration()
	if err != nil {
		t.Fatalf("ShareTTLDuration: %v", err)
	}
	if d.String() != "15m0s" {
		t.Errorf("default share_ttl = %s, want 15m0s", d)
	}

	// YAML override.
	cfg, err = Load(writeMinimalConfig(t, "seal:\n  share_ttl: 30m\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Seal.ShareTTL != "30m" {
		t.Errorf("file share_ttl = %q, want 30m", cfg.Seal.ShareTTL)
	}

	// Env override (highest precedence).
	t.Setenv("QRDB_SEAL_SHARE_TTL", "45s")
	cfg, err = Load(writeMinimalConfig(t, "seal:\n  share_ttl: 30m\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Seal.ShareTTL != "45s" {
		t.Errorf("env share_ttl = %q, want 45s (env override)", cfg.Seal.ShareTTL)
	}
}

// TestSealShareTTL_InvalidRejected verifies a bad duration is rejected by
// validation.
func TestSealShareTTL_InvalidRejected(t *testing.T) {
	_, err := Load(writeMinimalConfig(t, "seal:\n  enabled: true\n  strategy: passphrase\n  share_ttl: not-a-duration\n"))
	if err == nil {
		t.Fatal("expected validation error for invalid seal.share_ttl")
	}
}

// TestSealCredentialRotationCheckPeriod_DefaultAndOverride verifies that
// seal.credential_rotation_check_period defaults to one minute and that a
// file value overrides it.
func TestSealCredentialRotationCheckPeriod_DefaultAndOverride(t *testing.T) {
	cfg, err := Load(writeMinimalConfig(t, "logging:\n  level: info\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err := cfg.Seal.CredentialRotationCheckPeriodDuration()
	if err != nil {
		t.Fatalf("CredentialRotationCheckPeriodDuration: %v", err)
	}
	if d != time.Minute {
		t.Errorf("default credential_rotation_check_period = %s, want 1m0s", d)
	}

	cfg, err = Load(writeMinimalConfig(t, "seal:\n  credential_rotation_check_period: 10s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err = cfg.Seal.CredentialRotationCheckPeriodDuration()
	if err != nil {
		t.Fatalf("CredentialRotationCheckPeriodDuration: %v", err)
	}
	if d != 10*time.Second {
		t.Errorf("file credential_rotation_check_period = %s, want 10s", d)
	}
}

// TestSealCredentialRotationCheckPeriod_InvalidRejected verifies that a
// malformed or non-positive period is a *ValidationError naming the field.
func TestSealCredentialRotationCheckPeriod_InvalidRejected(t *testing.T) {
	for _, period := range []string{"not-a-duration", "0s", "-1m"} {
		_, err := Load(writeMinimalConfig(t, "seal:\n  enabled: true\n  strategy: passphrase\n  credential_rotation_check_period: "+period+"\n"))
		var verr *ValidationError
		if !errors.As(err, &verr) || verr.Field != "seal.credential_rotation_check_period" {
			t.Fatalf("period %q: err = %v, want a ValidationError on seal.credential_rotation_check_period", period, err)
		}
	}
}

// TestSealDrainTimeout_DefaultAndOverride covers the seal.drain_timeout
// plumbing: the 10s default when unset, a YAML override, and a
// QRDB_SEAL_DRAIN_TIMEOUT env override.
func TestSealDrainTimeout_DefaultAndOverride(t *testing.T) {
	cfg, err := Load(writeMinimalConfig(t, "logging:\n  level: info\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d, err := cfg.Seal.DrainTimeoutDuration(); err != nil || d != 10*time.Second {
		t.Errorf("default drain_timeout = %s, %v; want 10s", d, err)
	}

	cfg, err = Load(writeMinimalConfig(t, "seal:\n  drain_timeout: 30s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d, err := cfg.Seal.DrainTimeoutDuration(); err != nil || d != 30*time.Second {
		t.Errorf("file drain_timeout = %s, %v; want 30s", d, err)
	}

	t.Setenv("QRDB_SEAL_DRAIN_TIMEOUT", "45s")
	cfg, err = Load(writeMinimalConfig(t, "seal:\n  drain_timeout: 30s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Seal.DrainTimeout != "45s" {
		t.Errorf("env drain_timeout = %q, want 45s (env override)", cfg.Seal.DrainTimeout)
	}
}

// TestSealDrainTimeout_InvalidRejected verifies validation refuses a
// drain_timeout that does not parse or is not positive, naming the field.
func TestSealDrainTimeout_InvalidRejected(t *testing.T) {
	for _, v := range []string{"not-a-duration", "0s", "-1s"} {
		_, err := Load(writeMinimalConfig(t, "seal:\n  enabled: true\n  drain_timeout: "+v+"\n"))
		var verr *ValidationError
		if !errors.As(err, &verr) || verr.Field != "seal.drain_timeout" {
			t.Errorf("drain_timeout %q: Load = %v; want a ValidationError on seal.drain_timeout", v, err)
		}
	}
}

func TestSealConfig_DrainTimeoutDuration(t *testing.T) {
	if d, err := (SealConfig{}).DrainTimeoutDuration(); err != nil || d != 10*time.Second {
		t.Fatalf("unset = %s, %v; want the 10s default", d, err)
	}
	if d, err := (SealConfig{DrainTimeout: " 250ms "}).DrainTimeoutDuration(); err != nil || d != 250*time.Millisecond {
		t.Fatalf("250ms = %s, %v", d, err)
	}
	var verr *ValidationError
	if _, err := (SealConfig{DrainTimeout: "0"}).DrainTimeoutDuration(); !errors.As(err, &verr) {
		t.Fatalf("zero = %v; want a ValidationError", err)
	}
}

// TestQuotaReconcileInterval_DefaultAndOverride covers the
// tenant.quota_reconcile_interval plumbing: the 5m default when unset, a YAML
// override, and a QRDB_TENANT_QUOTA_RECONCILE_INTERVAL env override.
func TestQuotaReconcileInterval_DefaultAndOverride(t *testing.T) {
	// Default when the file omits the key.
	cfg, err := Load(writeMinimalConfig(t, "logging:\n  level: info\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err := cfg.Tenant.QuotaReconcileIntervalDuration()
	if err != nil {
		t.Fatalf("QuotaReconcileIntervalDuration: %v", err)
	}
	if d.String() != "5m0s" {
		t.Errorf("default quota_reconcile_interval = %s, want 5m0s", d)
	}

	// YAML override.
	cfg, err = Load(writeMinimalConfig(t, "tenant:\n  quota_reconcile_interval: 90s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tenant.QuotaReconcileInterval != "90s" {
		t.Errorf("file quota_reconcile_interval = %q, want 90s", cfg.Tenant.QuotaReconcileInterval)
	}
	d, err = cfg.Tenant.QuotaReconcileIntervalDuration()
	if err != nil {
		t.Fatalf("QuotaReconcileIntervalDuration: %v", err)
	}
	if d.String() != "1m30s" {
		t.Errorf("parsed quota_reconcile_interval = %s, want 1m30s", d)
	}

	// Env override (highest precedence).
	t.Setenv("QRDB_TENANT_QUOTA_RECONCILE_INTERVAL", "10s")
	cfg, err = Load(writeMinimalConfig(t, "tenant:\n  quota_reconcile_interval: 90s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tenant.QuotaReconcileInterval != "10s" {
		t.Errorf("env quota_reconcile_interval = %q, want 10s (env override)", cfg.Tenant.QuotaReconcileInterval)
	}
}

// TestQuotaReconcileInterval_InvalidRejected verifies a bad duration is
// rejected by validation.
func TestQuotaReconcileInterval_InvalidRejected(t *testing.T) {
	if _, err := Load(writeMinimalConfig(t, "tenant:\n  quota_reconcile_interval: not-a-duration\n")); err == nil {
		t.Fatal("expected validation error for invalid tenant.quota_reconcile_interval")
	}
	if _, err := Load(writeMinimalConfig(t, "tenant:\n  quota_reconcile_interval: -5m\n")); err == nil {
		t.Fatal("expected validation error for non-positive tenant.quota_reconcile_interval")
	}
}

// TestTenantIsolationPolicy_DefaultAndOverride covers tenant.isolation_policy:
// empty when unset, a YAML value, and a QRDB_TENANT_ISOLATION_POLICY env
// override.
func TestTenantIsolationPolicy_DefaultAndOverride(t *testing.T) {
	cfg, err := Load(writeMinimalConfig(t, "logging:\n  level: info\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tenant.IsolationPolicy != "" {
		t.Errorf("default isolation_policy = %q, want empty (the stored floor stands)", cfg.Tenant.IsolationPolicy)
	}

	cfg, err = Load(writeMinimalConfig(t, "tenant:\n  isolation_policy: operator-access\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tenant.IsolationPolicy != "operator-access" {
		t.Errorf("file isolation_policy = %q, want operator-access", cfg.Tenant.IsolationPolicy)
	}

	t.Setenv("QRDB_TENANT_ISOLATION_POLICY", "strict")
	cfg, err = Load(writeMinimalConfig(t, "tenant:\n  isolation_policy: operator-access\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tenant.IsolationPolicy != "strict" {
		t.Errorf("env isolation_policy = %q, want strict (env override)", cfg.Tenant.IsolationPolicy)
	}
}

// TestTenantIsolationPolicy_InvalidRejected verifies validation refuses a
// value that is not a settable floor.
func TestTenantIsolationPolicy_InvalidRejected(t *testing.T) {
	for _, v := range []string{"bogus", "Strict", "inherit"} {
		if _, err := Load(writeMinimalConfig(t, "tenant:\n  isolation_policy: "+v+"\n")); err == nil {
			t.Errorf("expected validation error for tenant.isolation_policy %q", v)
		}
	}
}
