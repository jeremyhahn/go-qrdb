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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// errFailingResolve is a sentinel returned by the failing resolver below.
var errFailingResolve = errors.New("backend unavailable")

// failingResolver always claims CanResolve but returns an error from Resolve.
type failingResolver struct{ scheme string }

func (r *failingResolver) Scheme() string           { return r.scheme }
func (r *failingResolver) CanResolve(_ string) bool { return true }
func (r *failingResolver) Resolve(_ context.Context, raw string) (*secret.SecretValue, error) {
	return nil, errFailingResolve
}

// ---------- Process: basic struct walking ----------

type flatConfig struct {
	Password string
	Host     string
}

func TestConfigProcessor_ResolvesNestedStringFields(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("resolved-pw")})

	cfg := &flatConfig{
		Password: "cred://db-pass",
		Host:     "localhost",
	}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", result.Resolved)
	}
	if cfg.Password != "resolved-pw" {
		t.Errorf("Password = %q, want resolved-pw", cfg.Password)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host must not be modified, got %q", cfg.Host)
	}
}

type nestedConfig struct {
	DB     dbConfig
	APIKey string
}

type dbConfig struct {
	Password string
	Port     int
}

func TestConfigProcessor_ResolvesNestedStruct(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("db-secret")})

	cfg := &nestedConfig{
		DB:     dbConfig{Password: "cred://db-pass", Port: 5432},
		APIKey: "cred://api-key",
	}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 2 {
		t.Errorf("Resolved = %d, want 2", result.Resolved)
	}
	if cfg.DB.Password != "db-secret" {
		t.Errorf("DB.Password = %q, want db-secret", cfg.DB.Password)
	}
	if cfg.APIKey != "db-secret" {
		t.Errorf("APIKey = %q, want db-secret", cfg.APIKey)
	}
	if cfg.DB.Port != 5432 {
		t.Error("non-string fields must not be modified")
	}
}

func TestConfigProcessor_SkipsNonSecretURIStrings(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("secret")})

	cfg := &flatConfig{
		Password: "cred://real-secret",
		Host:     "db.example.com",
	}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1 (only the URI field)", result.Resolved)
	}
	if cfg.Host != "db.example.com" {
		t.Errorf("non-URI host must not change, got %q", cfg.Host)
	}
}

func TestConfigProcessor_ResolvesPointerToStruct(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("ptr-secret")})

	inner := &dbConfig{Password: "cred://pass"}
	type wrapperConfig struct {
		Inner *dbConfig
	}
	cfg := &wrapperConfig{Inner: inner}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", result.Resolved)
	}
	if inner.Password != "ptr-secret" {
		t.Errorf("Password = %q, want ptr-secret", inner.Password)
	}
}

func TestConfigProcessor_ResolvesSliceElements(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("elem-secret")})

	type sliceCfg struct {
		Secrets []string
	}
	cfg := &sliceCfg{Secrets: []string{"cred://a", "plain", "cred://b"}}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 2 {
		t.Errorf("Resolved = %d, want 2", result.Resolved)
	}
	if cfg.Secrets[1] != "plain" {
		t.Error("non-URI element must not change")
	}
}

func TestConfigProcessor_ResolvesMapStringValues(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("map-secret")})

	type mapCfg struct {
		Params map[string]string
	}
	cfg := &mapCfg{Params: map[string]string{
		"password": "cred://db-pass",
		"host":     "db.local",
	}}
	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", result.Resolved)
	}
	if cfg.Params["password"] != "map-secret" {
		t.Errorf("Params[password] = %q, want map-secret", cfg.Params["password"])
	}
	if cfg.Params["host"] != "db.local" {
		t.Error("non-URI map value must not change")
	}
}

// ---------- WithFailClosed ----------

func TestConfigProcessor_WithFailClosed_ErrorOnUnresolved(t *testing.T) {
	reg := secret.NewResolverRegistry()
	// Resolver always claims CanResolve but always fails.
	reg.Register(&failingResolver{scheme: "failing"})

	cfg := &flatConfig{Password: "failing://secret"}
	proc := secret.NewConfigProcessor(reg, secret.WithFailClosed())

	result, err := proc.Process(context.Background(), cfg)
	if result == nil {
		t.Fatal("result must not be nil even when returning an error")
	}
	if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Errorf("expected ErrUnresolvedSecretURI, got %v", err)
	}
	if len(result.Unresolved) == 0 {
		t.Error("Unresolved must be non-empty")
	}
	if len(result.Errors) == 0 {
		t.Error("Errors must be non-empty")
	}
}

func TestConfigProcessor_WithFailClosed_NoErrorOnFullResolution(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("ok")})

	cfg := &flatConfig{Password: "cred://pass"}
	proc := secret.NewConfigProcessor(reg, secret.WithFailClosed())

	_, err := proc.Process(context.Background(), cfg)
	if err != nil {
		t.Errorf("expected nil error when all secrets resolved, got %v", err)
	}
}

// ---------- Process input validation ----------

func TestConfigProcessor_NilCtx(t *testing.T) {
	proc := secret.NewConfigProcessor(secret.NewResolverRegistry())
	_, err := proc.Process(nil, &flatConfig{}) //nolint:staticcheck
	if !errors.Is(err, secret.ErrNilContext) {
		t.Errorf("expected ErrNilContext, got %v", err)
	}
}

func TestConfigProcessor_NilConfig(t *testing.T) {
	proc := secret.NewConfigProcessor(secret.NewResolverRegistry())
	_, err := proc.Process(context.Background(), nil)
	var pie *secret.ProcessorInputError
	if !errors.As(err, &pie) {
		t.Errorf("expected ProcessorInputError for nil config, got %T: %v", err, err)
	}
}

func TestConfigProcessor_NonPointerConfig(t *testing.T) {
	proc := secret.NewConfigProcessor(secret.NewResolverRegistry())
	cfg := flatConfig{}
	_, err := proc.Process(context.Background(), cfg)
	var pie *secret.ProcessorInputError
	if !errors.As(err, &pie) {
		t.Errorf("expected ProcessorInputError for non-pointer config, got %T: %v", err, err)
	}
}

func TestConfigProcessor_PointerToNonStruct(t *testing.T) {
	proc := secret.NewConfigProcessor(secret.NewResolverRegistry())
	s := "not a struct"
	_, err := proc.Process(context.Background(), &s)
	var pie *secret.ProcessorInputError
	if !errors.As(err, &pie) {
		t.Errorf("expected ProcessorInputError for pointer-to-non-struct, got %T: %v", err, err)
	}
}
