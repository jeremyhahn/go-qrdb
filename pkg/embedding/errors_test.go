//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package embedding

import (
	"errors"
	"strings"
	"testing"
)

// sentinel used as an underlying cause in tests.
var errSentinel = errors.New("sentinel error")

func TestInvalidInputError_Error(t *testing.T) {
	e := &InvalidInputError{}
	if msg := e.Error(); !strings.Contains(msg, "empty") {
		t.Errorf("expected message to mention 'empty', got %q", msg)
	}
}

func TestEmptyResponseError_Error(t *testing.T) {
	e := &EmptyResponseError{}
	if msg := e.Error(); !strings.Contains(msg, "empty") {
		t.Errorf("expected message to mention 'empty', got %q", msg)
	}
}

func TestRetryExhaustedError_Error(t *testing.T) {
	e := &RetryExhaustedError{Attempts: 3, Cause: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "3") {
		t.Errorf("expected message to include attempt count, got %q", msg)
	}
	if !strings.Contains(msg, errSentinel.Error()) {
		t.Errorf("expected message to include cause, got %q", msg)
	}
}

func TestRetryExhaustedError_Unwrap(t *testing.T) {
	e := &RetryExhaustedError{Attempts: 1, Cause: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should find sentinel via Unwrap")
	}
}

func TestProviderUnavailableError_Error(t *testing.T) {
	e := &ProviderUnavailableError{BaseURL: "http://localhost:11434", Cause: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "http://localhost:11434") {
		t.Errorf("expected BaseURL in message, got %q", msg)
	}
	if !strings.Contains(msg, errSentinel.Error()) {
		t.Errorf("expected cause in message, got %q", msg)
	}
}

func TestProviderUnavailableError_Unwrap(t *testing.T) {
	e := &ProviderUnavailableError{Cause: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should find sentinel via Unwrap")
	}
}

func TestHTTPError_Error(t *testing.T) {
	e := &HTTPError{StatusCode: 401, Body: "unauthorized"}
	msg := e.Error()
	if !strings.Contains(msg, "401") {
		t.Errorf("expected status code in message, got %q", msg)
	}
	if !strings.Contains(msg, "unauthorized") {
		t.Errorf("expected body in message, got %q", msg)
	}
}

func TestDimensionMismatchError_Error(t *testing.T) {
	e := &DimensionMismatchError{Expected: 1536, Received: 768}
	msg := e.Error()
	if !strings.Contains(msg, "1536") || !strings.Contains(msg, "768") {
		t.Errorf("expected both dimensions in message, got %q", msg)
	}
}

func TestMarshalError_Error(t *testing.T) {
	e := &MarshalError{Cause: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "marshal") {
		t.Errorf("expected 'marshal' in message, got %q", msg)
	}
}

func TestMarshalError_Unwrap(t *testing.T) {
	e := &MarshalError{Cause: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should find sentinel via Unwrap")
	}
}

func TestUnmarshalError_Error(t *testing.T) {
	e := &UnmarshalError{Cause: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "unmarshal") {
		t.Errorf("expected 'unmarshal' in message, got %q", msg)
	}
}

func TestUnmarshalError_Unwrap(t *testing.T) {
	e := &UnmarshalError{Cause: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should find sentinel via Unwrap")
	}
}

func TestCacheCapacityError_Error(t *testing.T) {
	e := &CacheCapacityError{Capacity: -5}
	msg := e.Error()
	if !strings.Contains(msg, "-5") {
		t.Errorf("expected capacity value in message, got %q", msg)
	}
}

func TestProviderAlreadyRegisteredError_Error(t *testing.T) {
	e := &ProviderAlreadyRegisteredError{Name: "ollama"}
	msg := e.Error()
	if !strings.Contains(msg, "ollama") {
		t.Errorf("expected name in message, got %q", msg)
	}
}

func TestProviderNotFoundError_Error(t *testing.T) {
	e := &ProviderNotFoundError{Name: "vllm"}
	msg := e.Error()
	if !strings.Contains(msg, "vllm") {
		t.Errorf("expected name in message, got %q", msg)
	}
}

func TestCloseError_Error(t *testing.T) {
	e := &CloseError{Errors: []error{
		errors.New("transport closed"),
		errors.New("pool drained"),
	}}
	msg := e.Error()
	if !strings.Contains(msg, "transport closed") {
		t.Errorf("expected first error in message, got %q", msg)
	}
	if !strings.Contains(msg, "pool drained") {
		t.Errorf("expected second error in message, got %q", msg)
	}
}

func TestCloseError_ErrorsIs(t *testing.T) {
	inner := errors.New("specific error")
	e := &CloseError{Errors: []error{inner}}
	// errors.Join semantics: errors.Is walks Unwrap()[]error.
	if !errors.Is(e, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}
