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

// Sealed-window admin handler tests, plus the default-deny startup assertion.

package quic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleHealthRefusesWithoutAnAdminService covers the nil-service guard.
// A sealed node never reaches the handler -- the sealed-window gate refuses
// health first -- so a missing admin service is a wiring fault, refused with
// a typed error rather than a nil dereference or an invented answer.
func TestHandleHealthRefusesWithoutAnAdminService(t *testing.T) {
	s := &Server{}

	raw, err := s.handleHealth(context.Background(), nil)
	var want *ServiceNotConfiguredError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ServiceNotConfiguredError", err, err)
	}
	if want.Service != "admin" {
		t.Errorf("Service = %q, want %q", want.Service, "admin")
	}
	if raw != nil {
		t.Error("a refused health call must return no payload")
	}
}

// TestHandleHealthReturnsTheServiceReading covers the wired path, including
// that the leader is carried through -- a client uses it to redirect writes.
func TestHandleHealthReturnsTheServiceReading(t *testing.T) {
	s := &Server{adminSvc: &mockAdminService{
		healthFn: func(context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "healthy", Leader: true}, nil
		},
	}}

	raw, err := s.handleHealth(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleHealth: %v", err)
	}

	var resp pb.HealthResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("Status = %q, want %q", resp.Status, "healthy")
	}
	if !resp.Leader {
		t.Error("Leader = false, want true")
	}
}

// TestHandleHealthPropagatesAServiceFailure covers the error branch. A health
// check that reported success on a failing service is worse than no health
// check at all.
func TestHandleHealthPropagatesAServiceFailure(t *testing.T) {
	sentinel := errors.New("admin unavailable")
	s := &Server{adminSvc: &mockAdminService{
		healthFn: func(context.Context) (*service.HealthStatus, error) {
			return nil, sentinel
		},
	}}

	raw, err := s.handleHealth(context.Background(), nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
	if raw != nil {
		t.Error("a failed health check must return no payload")
	}
}

// TestHandleStatusRefusesWithoutAnAdminService covers Status's nil guard,
// which fails closed with a typed error exactly as Health's does.
func TestHandleStatusRefusesWithoutAnAdminService(t *testing.T) {
	s := &Server{}

	raw, err := s.handleStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("handleStatus must fail with no admin service")
	}
	var want *ServiceNotConfiguredError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ServiceNotConfiguredError", err, err)
	}
	if want.Service != "admin" {
		t.Errorf("Service = %q, want %q", want.Service, "admin")
	}
	if raw != nil {
		t.Error("a refused status must return no payload")
	}
}

// TestAssertMethodsAuthorizedAcceptsAFullyDeclaredMap covers the passing side
// of the startup assertion: every handler registered through the permission-
// recording helper is accounted for, so startup proceeds.
func TestAssertMethodsAuthorizedAcceptsAFullyDeclaredMap(t *testing.T) {
	s := &Server{
		handlers:    make(map[string]methodHandler),
		methodPerms: authmw.NewQUICMethodRegistry(),
	}
	s.handle("kv.get", permission.KVGet, func(context.Context, []byte) ([]byte, error) {
		return nil, nil
	})

	// Must not panic.
	s.assertMethodsAuthorized()
}

// TestAssertMethodsAuthorizedPanicsOnAnUndeclaredMethod covers the assertion
// itself -- the whole point of the mechanism.
//
// A handler assigned to s.handlers directly bypasses the permission registry
// and would be denied at runtime for every caller. Crashing at startup is the
// intended outcome: a method that is silently unreachable in production is far
// harder to diagnose than a refusal to boot.
func TestAssertMethodsAuthorizedPanicsOnAnUndeclaredMethod(t *testing.T) {
	s := &Server{
		handlers:    make(map[string]methodHandler),
		methodPerms: authmw.NewQUICMethodRegistry(),
	}
	// Bypass s.handle, which is exactly the mistake being guarded against.
	s.handlers["kv.smuggled"] = func(context.Context, []byte) ([]byte, error) {
		return nil, nil
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("assertMethodsAuthorized must panic on an undeclared method")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value %v is not a string", r)
		}
		if !strings.Contains(msg, "kv.smuggled") {
			t.Errorf("panic message %q does not name the offending method", msg)
		}
		if !strings.Contains(msg, "DEFAULT-DENY") {
			t.Errorf("panic message %q does not identify the assertion", msg)
		}
	}()

	s.assertMethodsAuthorized()
}

// TestCeremonyCANotConfiguredErrorNamesTheSubject covers the typed error. It
// exists to fail closed instead of dereferencing a nil CA, so its message has
// to point an operator at the missing WithCeremonyCA wiring.
func TestCeremonyCANotConfiguredErrorNamesTheSubject(t *testing.T) {
	msg := (&CeremonyCANotConfiguredError{}).Error()

	for _, want := range []string{"quic:", "ceremony CA", "not configured"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
}
