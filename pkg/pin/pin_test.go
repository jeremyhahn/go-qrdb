// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pin

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultLockoutConfig_Defaults(t *testing.T) {
	cfg := DefaultLockoutConfig()
	if cfg.MaxAttempts != 5 {
		t.Fatalf("MaxAttempts = %d, want 5", cfg.MaxAttempts)
	}
	if cfg.LockoutDuration != 5*time.Minute {
		t.Fatalf("LockoutDuration = %v, want 5m", cfg.LockoutDuration)
	}
	if !cfg.Backoff {
		t.Fatal("Backoff = false, want true")
	}
}

func TestValidatePINLength_AcceptsMinimum(t *testing.T) {
	if err := validatePINLength("123456"); err != nil {
		t.Fatalf("validatePINLength(6): %v", err)
	}
}

func TestValidatePINLength_AcceptsLonger(t *testing.T) {
	if err := validatePINLength("very-long-pin"); err != nil {
		t.Fatalf("validatePINLength(long): %v", err)
	}
}

func TestValidatePINLength_RejectsTooShort(t *testing.T) {
	if err := validatePINLength("12345"); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}

func TestValidatePINLength_RejectsEmpty(t *testing.T) {
	if err := validatePINLength(""); !errors.Is(err, ErrPINTooShort) {
		t.Fatalf("err = %v, want ErrPINTooShort", err)
	}
}
