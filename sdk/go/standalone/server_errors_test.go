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

package standalone

import (
	"errors"
	"strings"
	"testing"
)

func TestServerErrors_StorageCreateError(t *testing.T) {
	wrapped := errors.New("underlying")
	e := &StorageCreateError{Backend: "memory", Err: wrapped}
	if !strings.Contains(e.Error(), "memory") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap should match underlying")
	}
}

func TestServerErrors_EngineCreateError(t *testing.T) {
	wrapped := errors.New("engine boom")
	e := &EngineCreateError{Engine: "pebble", Err: wrapped}
	if !strings.Contains(e.Error(), "pebble") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestServerErrors_BarrierCreateError(t *testing.T) {
	wrapped := errors.New("barrier boom")
	e := &BarrierCreateError{Err: wrapped}
	if !strings.Contains(e.Error(), "barrier") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestServerErrors_ServerTransportCreateError(t *testing.T) {
	wrapped := errors.New("transport boom")
	e := &ServerTransportCreateError{Err: wrapped}
	if !strings.Contains(e.Error(), "transport") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestServerErrors_ServerClientCreateError(t *testing.T) {
	wrapped := errors.New("client boom")
	e := &ServerClientCreateError{Err: wrapped}
	if !strings.Contains(e.Error(), "client") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestServerErrors_ShutdownError(t *testing.T) {
	wrapped := errors.New("shutdown boom")
	e := &ShutdownError{Err: wrapped}
	if !strings.Contains(e.Error(), "shutdown") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestServerErrors_SealSentinelError(t *testing.T) {
	wrapped := errors.New("sentinel boom")
	e := &SealSentinelError{Err: wrapped}
	if !strings.Contains(e.Error(), "sentinel") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}
