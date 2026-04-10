// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"errors"
	"strings"
	"testing"
)

func TestInvalidShareError_Message(t *testing.T) {
	err := &InvalidShareError{Reason: "too short"}
	msg := err.Error()
	if !strings.Contains(msg, "invalid share") || !strings.Contains(msg, "too short") {
		t.Errorf("expected 'invalid share' and 'too short' in error, got: %s", msg)
	}
}

func TestBarrierSealedError_Message(t *testing.T) {
	err := &BarrierSealedError{}
	if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("expected 'sealed' in error, got: %s", err.Error())
	}
}

func TestDEKDerivationError_MessageAndUnwrap(t *testing.T) {
	inner := errors.New("crypto failure")
	err := &DEKDerivationError{TenantID: "tenant-1", Err: inner}
	msg := err.Error()
	if !strings.Contains(msg, "tenant-1") || !strings.Contains(msg, "crypto failure") {
		t.Errorf("expected tenant ID and cause in error, got: %s", msg)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return the inner error")
	}
}

func TestDEKDerivationError_NilErr(t *testing.T) {
	err := &DEKDerivationError{TenantID: "t", Err: nil}
	msg := err.Error()
	if !strings.Contains(msg, "t") {
		t.Errorf("expected tenant ID in error, got: %s", msg)
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap should return nil for nil Err")
	}
}

func TestInvalidConfigError_Message(t *testing.T) {
	err := &InvalidConfigError{Field: "SharesTotal", Reason: "must be positive"}
	msg := err.Error()
	if !strings.Contains(msg, "SharesTotal") || !strings.Contains(msg, "must be positive") {
		t.Errorf("expected field and reason in error, got: %s", msg)
	}
}

func TestTenantDEKNotFoundError_Message(t *testing.T) {
	err := &TenantDEKNotFoundError{TenantID: "org-42"}
	if !strings.Contains(err.Error(), "org-42") {
		t.Errorf("expected tenant ID in error, got: %s", err.Error())
	}
}
