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

//go:build !integration

package etcd

import (
	"errors"
	"fmt"
	"testing"
)

func TestStartError(t *testing.T) {
	inner := fmt.Errorf("connection refused")

	t.Run("Error", func(t *testing.T) {
		e := &StartError{Addr: "127.0.0.1:2379", Err: inner}
		want := "etcd: failed to start server on 127.0.0.1:2379: connection refused"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Unwrap", func(t *testing.T) {
		e := &StartError{Addr: "0.0.0.0:2379", Err: inner}
		if got := errors.Unwrap(e); got != inner {
			t.Errorf("Unwrap() = %v, want %v", got, inner)
		}
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		e := &StartError{Addr: "[::]:2379", Err: inner}
		wrapped := fmt.Errorf("wrapper: %w", e)
		var target *StartError
		if !errors.As(wrapped, &target) {
			t.Fatal("errors.As failed to find *StartError in chain")
		}
		if target.Addr != "[::]:2379" {
			t.Errorf("Addr = %q, want %q", target.Addr, "[::]:2379")
		}
	})

	t.Run("ErrorsIs", func(t *testing.T) {
		e := &StartError{Addr: "localhost:2379", Err: inner}
		if !errors.Is(e, inner) {
			t.Error("errors.Is should find inner error through Unwrap")
		}
	})
}

func TestInvalidRequestError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InvalidRequestError{Detail: "missing key"}
		want := "etcd: invalid request: missing key"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("EmptyDetail", func(t *testing.T) {
		e := &InvalidRequestError{Detail: ""}
		want := "etcd: invalid request: "
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		e := &InvalidRequestError{Detail: "bad range"}
		wrapped := fmt.Errorf("rpc: %w", e)
		var target *InvalidRequestError
		if !errors.As(wrapped, &target) {
			t.Fatal("errors.As failed to find *InvalidRequestError in chain")
		}
		if target.Detail != "bad range" {
			t.Errorf("Detail = %q, want %q", target.Detail, "bad range")
		}
	})
}
