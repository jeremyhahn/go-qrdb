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

package secret_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// ---------- Error() method coverage ----------

func TestInvalidSecretURIError_Error(t *testing.T) {
	e := &secret.InvalidSecretURIError{Raw: "bad://"}
	got := e.Error()
	if !strings.Contains(got, "bad://") {
		t.Errorf("Error() = %q, want to contain raw value", got)
	}
	if !strings.HasPrefix(got, "secret:") {
		t.Errorf("Error() = %q, want to start with 'secret:'", got)
	}
}

func TestSecretNotFoundError_Error(t *testing.T) {
	e := &secret.SecretNotFoundError{Scheme: "credential", Name: "db-pass"}
	got := e.Error()
	if !strings.Contains(got, "credential") || !strings.Contains(got, "db-pass") {
		t.Errorf("Error() = %q, want scheme and name in message", got)
	}
}

func TestResolutionFailedError_Error(t *testing.T) {
	inner := errors.New("timeout")
	e := &secret.ResolutionFailedError{Scheme: "kms", Raw: "kms://key", Err: inner}
	got := e.Error()
	if !strings.Contains(got, "kms") {
		t.Errorf("Error() = %q, want scheme in message", got)
	}
	if !strings.Contains(got, "timeout") {
		t.Errorf("Error() = %q, want inner error in message", got)
	}
}

func TestProcessorInputError_Error(t *testing.T) {
	e := &secret.ProcessorInputError{Msg: "config must not be nil"}
	got := e.Error()
	if !strings.HasPrefix(got, "secret: processor:") {
		t.Errorf("Error() = %q, want 'secret: processor:' prefix", got)
	}
}

// ---------- CredentialResolver.Scheme coverage ----------

func TestCredentialResolver_Scheme(t *testing.T) {
	res := secret.NewCredentialResolver(nil)
	if res.Scheme() != "credential" {
		t.Errorf("Scheme() = %q, want credential", res.Scheme())
	}
}

// ---------- processMap struct-in-map coverage ----------

type dbCreds struct {
	Password string
}

func TestConfigProcessor_ResolvesStructInMap(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("struct-map-secret")})

	type mapStructCfg struct {
		Backends map[string]dbCreds
	}
	cfg := &mapStructCfg{
		Backends: map[string]dbCreds{
			"primary": {Password: "cred://primary-pass"},
		},
	}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", result.Resolved)
	}
	if cfg.Backends["primary"].Password != "struct-map-secret" {
		t.Errorf("Backends[primary].Password = %q, want struct-map-secret",
			cfg.Backends["primary"].Password)
	}
}

// ---------- nil pointer field coverage ----------

func TestConfigProcessor_SkipsNilPointerField(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("v")})

	type ptrCfg struct {
		Inner *dbCreds // nil
		Name  string
	}
	cfg := &ptrCfg{Inner: nil, Name: "cred://name"}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Only Name should be resolved; nil Inner must be safely skipped.
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1 (nil pointer skipped)", result.Resolved)
	}
}

// ---------- interface field coverage (non-settable string inside interface{}) ----------

func TestConfigProcessor_SkipsUnsettableStringInInterface(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("v")})

	// An interface{} field holding a plain string value is not settable via
	// reflection (the string lives inside the interface, not at an addressable
	// location), so processString's CanSet guard must protect it.
	type ifaceCfg struct {
		Metadata interface{}
		Name     string
	}
	cfg := &ifaceCfg{
		Metadata: "cred://metadata", // held by value inside interface{} — not settable
		Name:     "cred://name",
	}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Name is resolved (settable), Metadata is NOT (inside interface → not settable).
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1 (interface string is not settable)", result.Resolved)
	}
	// The Metadata field must remain unchanged (processString skipped it).
	if cfg.Metadata != "cred://metadata" {
		t.Error("Metadata inside interface{} must not be modified")
	}
}
