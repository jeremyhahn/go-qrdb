//go:build !integration

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

package main

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// EmbeddedBarrierError
// ---------------------------------------------------------------------------

func TestEmbeddedBarrierError_Error(t *testing.T) {
	inner := errors.New("init failed")
	e := &EmbeddedBarrierError{Step: "initialize", Err: inner}
	want := "embedded: barrier initialize failed: init failed"
	if got := e.Error(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEmbeddedBarrierError_Error_DifferentStep(t *testing.T) {
	inner := errors.New("unseal error")
	e := &EmbeddedBarrierError{Step: "unseal", Err: inner}
	want := "embedded: barrier unseal failed: unseal error"
	if got := e.Error(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEmbeddedBarrierError_Unwrap(t *testing.T) {
	inner := errors.New("root cause")
	e := &EmbeddedBarrierError{Step: "rekey", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("Unwrap: expected %v, got %v", inner, unwrapped)
	}
}

func TestEmbeddedBarrierError_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel-err")
	e := &EmbeddedBarrierError{Step: "rotate", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("errors.Is should match sentinel via Unwrap")
	}
}

func TestEmbeddedBarrierError_ErrorsAs(t *testing.T) {
	inner := errors.New("failure")
	e := &EmbeddedBarrierError{Step: "seal", Err: inner}
	var target *EmbeddedBarrierError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should match EmbeddedBarrierError")
	}
	if target.Step != "seal" {
		t.Errorf("expected Step %q, got %q", "seal", target.Step)
	}
}
