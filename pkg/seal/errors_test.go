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

func TestGetError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("decrypt failed")
	e := &GetError{Key: "my-key", Err: inner}
	msg := e.Error()
	if !strings.Contains(msg, "my-key") {
		t.Errorf("expected key in error message, got: %s", msg)
	}
	if !strings.Contains(msg, "decrypt failed") {
		t.Errorf("expected inner error in message, got: %s", msg)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return the inner error")
	}
}

func TestPutError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("encrypt failed")
	e := &PutError{Key: "my-key", Err: inner}
	msg := e.Error()
	if !strings.Contains(msg, "my-key") {
		t.Errorf("expected key in error message, got: %s", msg)
	}
	if !strings.Contains(msg, "encrypt failed") {
		t.Errorf("expected inner error in message, got: %s", msg)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return the inner error")
	}
}
