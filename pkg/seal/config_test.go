// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"os"
	"testing"
)

// --------------------------------------------------------------------------
// config.go: PKCS11Config.UserPIN — 0%
// --------------------------------------------------------------------------

func TestPKCS11Config_UserPIN_Nil(t *testing.T) {
	var c *PKCS11Config
	if got := c.UserPIN(); got != "" {
		t.Errorf("nil receiver: expected empty string, got %q", got)
	}
}

func TestPKCS11Config_UserPIN_EmptyEnvVar(t *testing.T) {
	c := &PKCS11Config{UserPinEnvVar: ""}
	if got := c.UserPIN(); got != "" {
		t.Errorf("empty UserPinEnvVar: expected empty string, got %q", got)
	}
}

func TestPKCS11Config_UserPIN_FromEnv(t *testing.T) {
	const envKey = "TEST_QRDB_HSM_USER_PIN_42"
	t.Setenv(envKey, "supersecretpin")
	c := &PKCS11Config{UserPinEnvVar: envKey}
	if got := c.UserPIN(); got != "supersecretpin" {
		t.Errorf("expected supersecretpin, got %q", got)
	}
}

func TestPKCS11Config_UserPIN_UnsetEnv(t *testing.T) {
	const envKey = "TEST_QRDB_HSM_USER_PIN_UNSET_43"
	// Ensure the env var is definitely not set.
	os.Unsetenv(envKey) //nolint:errcheck
	c := &PKCS11Config{UserPinEnvVar: envKey}
	if got := c.UserPIN(); got != "" {
		t.Errorf("unset env: expected empty string, got %q", got)
	}
}
