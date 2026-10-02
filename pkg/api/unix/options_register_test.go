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

package unix

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The registration doubles below satisfy their interfaces by embedding them:
// Start only hands each to the gRPC registration, and no RPC is made, so a
// method reached by mistake panics on the nil embedded interface rather than
// answering with an invented result.
type (
	registerDEKSvc            struct{ service.DEKServicer }
	registerUserAuthFactorSvc struct {
		service.UserAuthFactorServicer
	}
	registerAuthFactorSvc struct{ service.AuthFactorServicer }
	registerLockSvc       struct{ service.LockServicer }
	registerRegistry      struct{ service.Registry }
	registerCeremonyCA    struct{ ceremony.CeremonyCA }
)

// TestStart_RegistersEveryOptionalService: a server given the DEK, user
// auth-factor, auth-factor and lock services, a service registry and a
// ceremony CA stores each option and starts with every one registered.
func TestStart_RegistersEveryOptionalService(t *testing.T) {
	dek, uaf, af := &registerDEKSvc{}, &registerUserAuthFactorSvc{}, &registerAuthFactorSvc{}
	lock, reg, ca := &registerLockSvc{}, &registerRegistry{}, &registerCeremonyCA{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, tempSocketPath(t),
		WithDEKService(dek),
		WithUserAuthFactorService(uaf),
		WithAuthFactorService(af),
		WithLock(lock),
		WithServiceRegistry(reg),
		WithCeremonyCA(ca),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.dekSvc != dek || srv.userAuthFactorSvc != uaf || srv.authFactorSvc != af ||
		srv.lockSvc != lock || srv.registry != reg || srv.ceremonyCA != ca {
		t.Fatal("an option did not set its field")
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()
}

// TestStart_RefusesAnUnusableSocketPath: a socket path that names a
// non-empty directory cannot be cleared, and one longer than a Unix socket
// address holds cannot be bound; both fail Start with *StartError and leave no listener.
func TestStart_RefusesAnUnusableSocketPath(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(occupied, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(dir, strings.Repeat("x", 200))
	for name, path := range map[string]string{
		"a non-empty directory":   occupied,
		"a path too long to bind": long,
	} {
		srv := NewServer(newMockKVService(), &mockAdminService{}, path, WithTLS(selfSignedTLSConfig(t)))
		err := srv.Start()
		var start *StartError
		if !errors.As(err, &start) || start.SocketPath != path {
			t.Fatalf("%s: Start = %v, want *StartError", name, err)
		}
		if srv.listener != nil {
			t.Fatalf("%s: a listener was left after a failed Start", name)
		}
	}
}
