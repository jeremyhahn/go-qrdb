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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// vaultScheme is the reference scheme used by the protected-target tests.
const vaultScheme = "vault://"

// vaultPlaintext is the secret every vaultResolver hands back. It is
// deliberately distinctive so a heap scan over the config struct can look for
// it verbatim.
const vaultPlaintext = "s3cr3t-plaintext"

// vaultResolver resolves any "vault://" reference to vaultPlaintext, tagging
// the result StrategyCredential so metadata propagation is observable.
type vaultResolver struct {
	err error
}

func (r *vaultResolver) Scheme() string { return "vault" }

func (r *vaultResolver) CanResolve(raw string) bool { return strings.HasPrefix(raw, vaultScheme) }

func (r *vaultResolver) Resolve(ctx context.Context, raw string) (*secret.SecretValue, error) {
	if r.err != nil {
		return nil, r.err
	}
	return secret.NewSecretValue(ctx, raw, []byte(vaultPlaintext), secret.StrategyCredential)
}

// newVaultRegistry returns a registry holding a single healthy vaultResolver.
func newVaultRegistry() *secret.ResolverRegistry {
	reg := secret.NewResolverRegistry()
	reg.Register(&vaultResolver{})
	return reg
}

// plaintextOf reads a SecretValue through the enclave-respecting accessor.
func plaintextOf(t *testing.T, sv *secret.SecretValue) string {
	t.Helper()
	var got string
	if err := sv.WithBytes(t.Context(), func(b []byte) error {
		got = string(b)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	return got
}

// stringsContaining collects the dot-notation paths of every exported string
// reachable from v whose content contains want. It is how the tests below
// assert that a resolved secret never comes to rest in a plain Go string.
func stringsContaining(v reflect.Value, want, path string, out *[]string) {
	switch v.Kind() {
	case reflect.String:
		if strings.Contains(v.String(), want) {
			*out = append(*out, path)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			stringsContaining(v.Elem(), want, path, out)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			stringsContaining(v.Field(i), want, path+"."+t.Field(i).Name, out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			stringsContaining(v.Index(i), want, path, out)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			stringsContaining(iter.Value(), want, path, out)
		}
	}
}

// ---------- protected target: the plaintext never becomes a string ----------

type vaultConfig struct {
	Host     string
	Password *secret.SecretValue
}

func TestProcess_ProtectedTarget_PointerFieldResolvesIntoEnclave(t *testing.T) {
	cfg := &vaultConfig{
		Host:     "localhost",
		Password: &secret.SecretValue{Raw: vaultScheme + "db-key"},
	}

	result, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Fatalf("Resolved = %d, want 1", result.Resolved)
	}
	if got := plaintextOf(t, cfg.Password); got != vaultPlaintext {
		t.Errorf("plaintext = %q, want %q", got, vaultPlaintext)
	}
	if cfg.Password.Strategy != secret.StrategyCredential {
		t.Errorf("Strategy = %q, want %q", cfg.Password.Strategy, secret.StrategyCredential)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host must not be modified, got %q", cfg.Host)
	}
}

func TestProcess_ProtectedTarget_PreservesRawReference(t *testing.T) {
	// Regression guard: the walker used to descend into a SecretValue and treat
	// its exported Raw field as an ordinary string, overwriting the reference
	// with the plaintext and leaving the enclave empty — strictly worse than a
	// plain string field.
	const ref = vaultScheme + "db-key"
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: ref}}

	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if cfg.Password.Raw != ref {
		t.Fatalf("Raw = %q, want the reference %q", cfg.Password.Raw, ref)
	}
	if got := plaintextOf(t, cfg.Password); got != vaultPlaintext {
		t.Fatalf("enclave = %q, want %q", got, vaultPlaintext)
	}
}

type valueTargetConfig struct {
	Password secret.SecretValue
}

func TestProcess_ProtectedTarget_ValueFieldResolvesInPlace(t *testing.T) {
	cfg := &valueTargetConfig{Password: secret.SecretValue{Raw: vaultScheme + "db-key"}}

	result, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Fatalf("Resolved = %d, want 1", result.Resolved)
	}
	if got := plaintextOf(t, &cfg.Password); got != vaultPlaintext {
		t.Errorf("plaintext = %q, want %q", got, vaultPlaintext)
	}
}

type deepConfig struct {
	Primary  vaultConfig
	Replicas []vaultConfig
	ByName   map[string]secret.SecretValue
	ByRef    map[string]*secret.SecretValue
}

func TestProcess_ProtectedTarget_NoPlaintextStringAnywhere(t *testing.T) {
	// The audit's acceptance criterion: a consumer struct receives its resolved
	// secrets without any of them coming to rest in a plain string field.
	cfg := &deepConfig{
		Primary:  vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "primary"}},
		Replicas: []vaultConfig{{Password: &secret.SecretValue{Raw: vaultScheme + "replica"}}},
		ByName:   map[string]secret.SecretValue{"a": {Raw: vaultScheme + "a"}},
		ByRef:    map[string]*secret.SecretValue{"b": {Raw: vaultScheme + "b"}},
	}

	result, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 4 {
		t.Fatalf("Resolved = %d, want 4 (struct, slice, map value, map pointer)", result.Resolved)
	}

	var leaked []string
	stringsContaining(reflect.ValueOf(cfg), vaultPlaintext, "cfg", &leaked)
	if len(leaked) != 0 {
		t.Fatalf("plaintext came to rest in plain string fields: %v", leaked)
	}

	byName := cfg.ByName["a"]
	for name, sv := range map[string]*secret.SecretValue{
		"Primary":  cfg.Primary.Password,
		"Replicas": cfg.Replicas[0].Password,
		"ByName":   &byName,
		"ByRef":    cfg.ByRef["b"],
	} {
		if got := plaintextOf(t, sv); got != vaultPlaintext {
			t.Errorf("%s: plaintext = %q, want %q", name, got, vaultPlaintext)
		}
	}
}

func TestProcess_ProtectedTarget_NilPointerIsAnAbsentSecret(t *testing.T) {
	// The nil pointer is the one silent case: it is how an optional secret is
	// spelled, so it must not fail a closed boot.
	cfg := &vaultConfig{Password: nil}
	result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
		Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 0 || len(result.Unresolved) != 0 || len(result.Errors) != 0 {
		t.Errorf("result = %+v, want a clean no-op", result)
	}
}

func TestProcess_ProtectedTarget_MustReferenceAResolvableSecret(t *testing.T) {
	// Anything a protected target cannot resolve is reported, never skipped:
	// an empty value and an unclaimed scheme deliver no secret at all, and an
	// inline secret leaves the plaintext sitting in Raw — an ordinary Go string
	// that outlives the walk, which is exactly what the target exists to avoid.
	cases := map[string]string{
		"empty raw":      "",
		"unknown scheme": "postgres://db-key",
		"bare value":     vaultPlaintext,
		"inline literal": "literal://" + vaultPlaintext,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &vaultConfig{Password: &secret.SecretValue{Raw: raw}}
			result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
				Process(context.Background(), cfg)
			if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
				t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
			}
			if result.Resolved != 0 {
				t.Errorf("Resolved = %d, want 0", result.Resolved)
			}
			if len(result.Unresolved) != 1 || result.Unresolved[0] != "Password" {
				t.Errorf("Unresolved = %v, want [Password]", result.Unresolved)
			}
			var unresolvable *secret.UnresolvableTargetError
			if !errors.As(errors.Join(result.Errors...), &unresolvable) {
				t.Fatalf("Errors = %v, want an UnresolvableTargetError", result.Errors)
			}
			if unresolvable.Path != "Password" {
				t.Errorf("Path = %q, want Password", unresolvable.Path)
			}
			if strings.Contains(unresolvable.Error(), vaultPlaintext) {
				t.Errorf("Error() = %q, must never carry the secret itself", unresolvable.Error())
			}
			if got := plaintextOf(t, cfg.Password); got != "" {
				t.Errorf("nothing may be sealed, got %q", got)
			}
		})
	}
}

func TestProcess_ProtectedTarget_InlineLiteralIsNeverSealed(t *testing.T) {
	// The literal path is the one the reviewer flagged: sealing it would leave
	// the plaintext in Raw as well as the enclave. Refusing it is the only
	// outcome that keeps the target's promise, and the refusal must not leak
	// the value through any display path either.
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: "literal://" + vaultPlaintext}}
	result, _ := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg)
	if result.Resolved != 0 {
		t.Fatalf("Resolved = %d, want 0", result.Resolved)
	}
	for name, out := range map[string]string{
		"Redacted":  cfg.Password.Redacted(),
		"%v":        fmt.Sprintf("%v", cfg.Password),
		"error":     errors.Join(result.Errors...).Error(),
		"processed": fmt.Sprintf("%v", result.Unresolved),
	} {
		if strings.Contains(out, vaultPlaintext) {
			t.Errorf("%s exposed the inline secret: %s", name, out)
		}
	}
}

func TestProcess_ProtectedTarget_ResolutionFailureIsReported(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&vaultResolver{err: errors.New("backend down")})
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}

	result, err := secret.NewConfigProcessor(reg, secret.WithFailClosed()).Process(context.Background(), cfg)
	if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
	}
	if len(result.Unresolved) != 1 || result.Unresolved[0] != "Password" {
		t.Errorf("Unresolved = %v, want [Password]", result.Unresolved)
	}
	if len(result.Errors) != 1 {
		t.Errorf("Errors = %v, want exactly one", result.Errors)
	}
}

func TestProcess_ProtectedTarget_ReresolveReplacesEnclave(t *testing.T) {
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
	proc := secret.NewConfigProcessor(newVaultRegistry())

	for i := 0; i < 3; i++ {
		if _, err := proc.Process(context.Background(), cfg); err != nil {
			t.Fatalf("Process #%d: %v", i, err)
		}
		if got := plaintextOf(t, cfg.Password); got != vaultPlaintext {
			t.Fatalf("Process #%d: plaintext = %q, want %q", i, got, vaultPlaintext)
		}
	}
}

type anyConfig struct {
	Any any
}

func TestProcess_ProtectedTarget_InInterface(t *testing.T) {
	// A SecretValue boxed in an interface is not addressable, so it is resolved
	// through an addressable copy and assigned back; a *SecretValue in the same
	// interface is reachable and resolves where it sits.
	byValue := &anyConfig{Any: secret.SecretValue{Raw: vaultScheme + "db-key"}}
	result, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), byValue)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Fatalf("boxed value: Resolved = %d, want 1", result.Resolved)
	}
	boxed, ok := byValue.Any.(secret.SecretValue)
	if !ok {
		t.Fatalf("Any = %T, want secret.SecretValue", byValue.Any)
	}
	if got := plaintextOf(t, &boxed); got != vaultPlaintext {
		t.Errorf("boxed value: plaintext = %q, want %q", got, vaultPlaintext)
	}

	ref := &secret.SecretValue{Raw: vaultScheme + "db-key"}
	byPointer := &anyConfig{Any: ref}
	result, err = secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), byPointer)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Fatalf("boxed pointer: Resolved = %d, want 1", result.Resolved)
	}
	if got := plaintextOf(t, ref); got != vaultPlaintext {
		t.Errorf("plaintext = %q, want %q", got, vaultPlaintext)
	}
}

func TestProcess_ProtectedTarget_InMapInterfaceValue(t *testing.T) {
	cfg := &struct{ Extra map[string]any }{
		Extra: map[string]any{
			"byValue":   secret.SecretValue{Raw: vaultScheme + "a"},
			"byPointer": &secret.SecretValue{Raw: vaultScheme + "b"},
			"plain":     "not-a-reference",
		},
	}

	result, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 2 {
		t.Fatalf("Resolved = %d, want 2", result.Resolved)
	}
	byValue, ok := cfg.Extra["byValue"].(secret.SecretValue)
	if !ok {
		t.Fatalf("byValue = %T, want secret.SecretValue", cfg.Extra["byValue"])
	}
	if got := plaintextOf(t, &byValue); got != vaultPlaintext {
		t.Errorf("byValue: plaintext = %q, want %q", got, vaultPlaintext)
	}
	byPointer, ok := cfg.Extra["byPointer"].(*secret.SecretValue)
	if !ok {
		t.Fatalf("byPointer = %T, want *secret.SecretValue", cfg.Extra["byPointer"])
	}
	if got := plaintextOf(t, byPointer); got != vaultPlaintext {
		t.Errorf("byPointer: plaintext = %q, want %q", got, vaultPlaintext)
	}
}

// nestedAny is boxed by value inside an interface, which makes its own
// interface field unsettable — the one shape where the walk can see a protected
// target but cannot write to it.
type nestedAny struct {
	Inner any
}

func TestProcess_ProtectedTarget_UnwritableFailsClosed(t *testing.T) {
	cfg := &anyConfig{Any: nestedAny{Inner: secret.SecretValue{Raw: vaultScheme + "db-key"}}}

	result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
		Process(context.Background(), cfg)
	if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Fatalf("err = %v, want ErrUnresolvedSecretURI — an unwritable target must not pass fail-closed", err)
	}
	if result.Resolved != 0 {
		t.Errorf("Resolved = %d, want 0", result.Resolved)
	}
	if len(result.Unresolved) != 1 || result.Unresolved[0] != "Any.Inner" {
		t.Errorf("Unresolved = %v, want [Any.Inner]", result.Unresolved)
	}
	var unwritable *secret.UnwritableTargetError
	if !errors.As(errors.Join(result.Errors...), &unwritable) {
		t.Fatalf("Errors = %v, want an UnwritableTargetError", result.Errors)
	}
	if unwritable.Path != "Any.Inner" {
		t.Errorf("Path = %q, want Any.Inner", unwritable.Path)
	}
	if !strings.Contains(unwritable.Error(), "Any.Inner") {
		t.Errorf("Error() = %q, want it to name the path", unwritable.Error())
	}
}

func TestProcess_ProtectedTarget_UnwritableIsAlwaysReported(t *testing.T) {
	// A target the walk cannot write to is reported whatever it holds: it can
	// never be resolved, so treating an empty or unclaimed one as harmless
	// would reintroduce exactly the silence this fixes.
	cases := map[string]any{
		"reference":      nestedAny{Inner: secret.SecretValue{Raw: vaultScheme + "db-key"}},
		"empty raw":      nestedAny{Inner: secret.SecretValue{}},
		"unknown scheme": nestedAny{Inner: secret.SecretValue{Raw: "postgres://db-key"}},
	}
	for name, inner := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &anyConfig{Any: inner}
			result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
				Process(context.Background(), cfg)
			if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
				t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
			}
			if len(result.Unresolved) != 1 || result.Unresolved[0] != "Any.Inner" {
				t.Errorf("Unresolved = %v, want [Any.Inner]", result.Unresolved)
			}
			var unwritable *secret.UnwritableTargetError
			if !errors.As(errors.Join(result.Errors...), &unwritable) {
				t.Fatalf("Errors = %v, want an UnwritableTargetError", result.Errors)
			}
		})
	}
}

// ---------- WithNoPlaintextStrings: the migration ratchet ----------

type mixedConfig struct {
	Legacy    string
	Protected *secret.SecretValue
	Extra     map[string]string
}

func TestProcess_NoPlaintextStrings_LeavesStringFieldsUntouched(t *testing.T) {
	const ref = vaultScheme + "db-key"
	cfg := &mixedConfig{
		Legacy:    ref,
		Protected: &secret.SecretValue{Raw: ref},
		Extra:     map[string]string{"api": ref},
	}

	result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithNoPlaintextStrings()).
		Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if cfg.Legacy != ref {
		t.Errorf("Legacy = %q, want the untouched reference %q", cfg.Legacy, ref)
	}
	if cfg.Extra["api"] != ref {
		t.Errorf("Extra[api] = %q, want the untouched reference %q", cfg.Extra["api"], ref)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1 (the protected target only)", result.Resolved)
	}
	if got := plaintextOf(t, cfg.Protected); got != vaultPlaintext {
		t.Errorf("protected target must still resolve, got %q", got)
	}

	want := map[string]bool{"Legacy": true, "Extra[api]": true}
	if len(result.PlaintextSkipped) != len(want) {
		t.Fatalf("PlaintextSkipped = %v, want %v", result.PlaintextSkipped, want)
	}
	for _, path := range result.PlaintextSkipped {
		if !want[path] {
			t.Errorf("unexpected PlaintextSkipped entry %q", path)
		}
	}
	// PlaintextSkipped is documented as a subset of Unresolved.
	unresolved := make(map[string]bool, len(result.Unresolved))
	for _, path := range result.Unresolved {
		unresolved[path] = true
	}
	for _, path := range result.PlaintextSkipped {
		if !unresolved[path] {
			t.Errorf("%q in PlaintextSkipped but not in Unresolved", path)
		}
	}
}

func TestProcess_NoPlaintextStrings_FailsClosedWhenCombined(t *testing.T) {
	cfg := &mixedConfig{Legacy: vaultScheme + "db-key"}

	result, err := secret.NewConfigProcessor(newVaultRegistry(),
		secret.WithNoPlaintextStrings(), secret.WithFailClosed()).
		Process(context.Background(), cfg)
	if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
	}
	if len(result.PlaintextSkipped) != 1 {
		t.Errorf("PlaintextSkipped = %v, want one entry", result.PlaintextSkipped)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want none — nothing failed, the field was refused", result.Errors)
	}
}

func TestProcess_PlaintextStringPathUnchangedByDefault(t *testing.T) {
	// The deprecated plain path is additive-compatible: with no options the
	// processor still writes plaintext into a string field exactly as before.
	cfg := &mixedConfig{
		Legacy: vaultScheme + "db-key",
		Extra:  map[string]string{"api": vaultScheme + "api-key"},
	}

	result, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if cfg.Legacy != vaultPlaintext {
		t.Errorf("Legacy = %q, want %q", cfg.Legacy, vaultPlaintext)
	}
	if cfg.Extra["api"] != vaultPlaintext {
		t.Errorf("Extra[api] = %q, want %q", cfg.Extra["api"], vaultPlaintext)
	}
	if len(result.PlaintextSkipped) != 0 {
		t.Errorf("PlaintextSkipped = %v, want empty", result.PlaintextSkipped)
	}
}

// ---------- configuration codecs ----------

type yamlConfig struct {
	Database struct {
		Password *secret.SecretValue `yaml:"password" json:"password"`
	} `yaml:"database" json:"database"`
}

func TestSecretValue_YAMLDecodeThenProcess(t *testing.T) {
	const doc = "database:\n  password: \"vault://db-key\"\n"

	var cfg yamlConfig
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.Database.Password == nil || cfg.Database.Password.Raw != vaultScheme+"db-key" {
		t.Fatalf("decoded Raw = %+v, want the reference", cfg.Database.Password)
	}
	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), &cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := plaintextOf(t, cfg.Database.Password); got != vaultPlaintext {
		t.Errorf("plaintext = %q, want %q", got, vaultPlaintext)
	}
}

func TestSecretValue_JSONDecodeThenProcess(t *testing.T) {
	const doc = `{"database":{"password":"vault://db-key"}}`

	var cfg yamlConfig
	if err := json.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if cfg.Database.Password == nil || cfg.Database.Password.Raw != vaultScheme+"db-key" {
		t.Fatalf("decoded Raw = %+v, want the reference", cfg.Database.Password)
	}
	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), &cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := plaintextOf(t, cfg.Database.Password); got != vaultPlaintext {
		t.Errorf("plaintext = %q, want %q", got, vaultPlaintext)
	}
}

func TestSecretValue_MarshalEmitsReferenceNotPlaintext(t *testing.T) {
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}

	asJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	asYAML, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	for name, out := range map[string]string{"json": string(asJSON), "yaml": string(asYAML)} {
		if strings.Contains(out, vaultPlaintext) {
			t.Errorf("%s output leaked the plaintext: %s", name, out)
		}
		if !strings.Contains(out, vaultScheme+"db-key") {
			t.Errorf("%s output lost the reference: %s", name, out)
		}
	}
}

func TestSecretValue_DecodeRejectsNonScalar(t *testing.T) {
	t.Run("yaml", func(t *testing.T) {
		var sv secret.SecretValue
		err := yaml.Unmarshal([]byte("{a: 1}\n"), &sv)
		var decodeErr *secret.SecretValueDecodeError
		if !errors.As(err, &decodeErr) {
			t.Fatalf("err = %v, want SecretValueDecodeError", err)
		}
		if decodeErr.Format != "yaml" {
			t.Errorf("Format = %q, want yaml", decodeErr.Format)
		}
		if decodeErr.Unwrap() == nil {
			t.Error("Unwrap must expose the decoder error")
		}
	})
	t.Run("json", func(t *testing.T) {
		var sv secret.SecretValue
		err := json.Unmarshal([]byte(`{"a":1}`), &sv)
		var decodeErr *secret.SecretValueDecodeError
		if !errors.As(err, &decodeErr) {
			t.Fatalf("err = %v, want SecretValueDecodeError", err)
		}
		if decodeErr.Format != "json" {
			t.Errorf("Format = %q, want json", decodeErr.Format)
		}
		if !strings.Contains(decodeErr.Error(), "reference string") {
			t.Errorf("Error() = %q, want it to explain the expected shape", decodeErr.Error())
		}
	})
}

func TestSecretValue_JSONNullIsRejected(t *testing.T) {
	// A value-typed field is the only shape encoding/json hands "null" to; a
	// pointer field is set to nil without the unmarshaler ever being called.
	var cfg struct {
		Password secret.SecretValue `json:"password"`
	}
	cfg.Password = *mustSecretValue(t, vaultScheme+"old", []byte(vaultPlaintext), secret.StrategyCredential)

	err := json.Unmarshal([]byte(`{"password":null}`), &cfg)
	var decodeErr *secret.SecretValueDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("err = %v, want SecretValueDecodeError", err)
	}
	if decodeErr.Format != "json" {
		t.Errorf("Format = %q, want json", decodeErr.Format)
	}
	if !strings.Contains(decodeErr.Error(), "null") {
		t.Errorf("Error() = %q, want it to name the offending token", decodeErr.Error())
	}
	if cfg.Password.Raw != vaultScheme+"old" {
		t.Errorf("Raw = %q, a rejected decode must not clear the value", cfg.Password.Raw)
	}
}

func TestSecretValue_JSONNullPointerFieldIsAbsent(t *testing.T) {
	// The documented way to express an optional secret: a pointer field, where
	// null means absent and never reaches UnmarshalJSON.
	var cfg yamlConfig
	if err := json.Unmarshal([]byte(`{"database":{"password":null}}`), &cfg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if cfg.Database.Password != nil {
		t.Errorf("Password = %+v, want nil", cfg.Database.Password)
	}
	result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
		Process(context.Background(), &cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 0 || len(result.Unresolved) != 0 {
		t.Errorf("Resolved = %d, Unresolved = %v, want an absent secret to be a no-op", result.Resolved, result.Unresolved)
	}
}

func TestSecretValue_YAMLNullIsRefusedAtTheWalk(t *testing.T) {
	// yaml.v3 zeroes a null destination without consulting the unmarshaler, so
	// UnmarshalYAML never sees `password:` or `password: null` and cannot
	// reject them the way UnmarshalJSON rejects a null token. The walk closes
	// the gap instead: a value-typed target that decoded to nothing is refused,
	// so a required secret cannot be deleted or nulled into a successful boot.
	for name, doc := range map[string]string{
		"explicit null": "password: null\n",
		"empty value":   "password:\n",
		"absent key":    "host: localhost\n",
	} {
		t.Run(name, func(t *testing.T) {
			var cfg struct {
				Host     string             `yaml:"host"`
				Password secret.SecretValue `yaml:"password"`
			}
			if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
				t.Fatalf("yaml.Unmarshal: %v", err)
			}
			if cfg.Password.Raw != "" {
				t.Fatalf("Raw = %q, want empty", cfg.Password.Raw)
			}
			result, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
				Process(context.Background(), &cfg)
			if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
				t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
			}
			var unresolvable *secret.UnresolvableTargetError
			if !errors.As(errors.Join(result.Errors...), &unresolvable) {
				t.Fatalf("Errors = %v, want an UnresolvableTargetError", result.Errors)
			}
		})
	}
}

func TestSecretValue_YAMLNullPointerFieldIsAbsent(t *testing.T) {
	// The optional spelling: a pointer field left null stays nil, and a closed
	// boot is happy with it.
	var cfg yamlConfig
	if err := yaml.Unmarshal([]byte("database:\n  password:\n"), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.Database.Password != nil {
		t.Fatalf("Password = %+v, want nil", cfg.Database.Password)
	}
	if _, err := secret.NewConfigProcessor(newVaultRegistry(), secret.WithFailClosed()).
		Process(context.Background(), &cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}
}

func TestSecretValue_DecodeDiscardsPreviousResolution(t *testing.T) {
	sv := mustSecretValue(t, vaultScheme+"old", []byte(vaultPlaintext), secret.StrategyCredential)
	sv.ExpiresAt = sv.ExpiresAt.AddDate(1, 0, 0)

	if err := yaml.Unmarshal([]byte("vault://new\n"), sv); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if sv.Raw != vaultScheme+"new" {
		t.Errorf("Raw = %q, want the new reference", sv.Raw)
	}
	if sv.Strategy != "" {
		t.Errorf("Strategy = %q, want it reset by the decode", sv.Strategy)
	}
	if !sv.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want it reset by the decode", sv.ExpiresAt)
	}
	if got := plaintextOf(t, sv); got != "" {
		t.Errorf("the previous enclave must be destroyed, got %q", got)
	}
}

// ---------- Redacted ----------

func TestSecretValue_Redacted(t *testing.T) {
	cases := map[string]struct {
		raw      string
		strategy secret.ResolvedStrategy
		want     string
	}{
		// Shown: a resolver vouched for the reference.
		"resolved reference":  {raw: vaultScheme + "db-key", strategy: secret.StrategyCredential, want: vaultScheme + "db-key"},
		"resolved credential": {raw: "credential://ca-key", strategy: secret.StrategyCredential, want: "credential://ca-key"},
		"resolved kms":        {raw: "kms://ca-key", strategy: secret.StrategyLocalKMS, want: "kms://ca-key"},
		// Redacted: Raw is, or may be, the secret itself.
		"literal scheme":     {raw: "literal://hunter2", strategy: secret.StrategyLiteral, want: "[redacted]"},
		"bare value":         {raw: "hunter2", strategy: secret.StrategyLiteral, want: "[redacted]"},
		"empty":              {raw: "", strategy: secret.StrategyLiteral, want: "[redacted]"},
		"scheme only":        {raw: "://db-key", strategy: secret.StrategyCredential, want: "[redacted]"},
		"no separator":       {raw: "vault:db-key", strategy: secret.StrategyCredential, want: "[redacted]"},
		"unclaimed scheme":   {raw: "unknown://hunter2", strategy: secret.StrategyLiteral, want: "[redacted]"},
		"url with password":  {raw: "https://user:hunter2@example.test", strategy: secret.StrategyLiteral, want: "[redacted]"},
		"unresolved":         {raw: vaultScheme + "db-key", want: "[redacted]"},
		"unresolved literal": {raw: "literal://hunter2", want: "[redacted]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sv := &secret.SecretValue{Raw: tc.raw, Strategy: tc.strategy}
			if got := sv.Redacted(); got != tc.want {
				t.Errorf("Redacted() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecretValue_Redacted_NilReceiver(t *testing.T) {
	var sv *secret.SecretValue
	if got := sv.Redacted(); got != "[redacted]" {
		t.Errorf("Redacted() = %q, want [redacted]", got)
	}
}

func TestSecretValue_Redacted_NeverRevealsPlaintext(t *testing.T) {
	sv := mustSecretValue(t, "literal://"+vaultPlaintext, []byte(vaultPlaintext), secret.StrategyLiteral)
	if strings.Contains(sv.Redacted(), vaultPlaintext) {
		t.Errorf("Redacted() = %q, must not reveal a literal's plaintext", sv.Redacted())
	}
}

// ---------- SealFailedError ----------

// TestSealFailedError_Error_NeverRevealsPlaintext is the go-qrdb#114 round 2
// regression, tightened in round 3: NewSecretValue only reaches
// secure.NewSecret AFTER resolving the value, so for a literal reference the
// raw reference IS the plaintext secret, not a pointer to one. Redaction now
// happens once, at construction, and only the already-redacted result is
// ever stored on the error's exported Ref field -- Ref is built here via
// SecretValue.Redacted(), the same exported, independently-tested provenance
// rule NewSecretValue's sealing-failure path applies internally, so this
// pins the CONTRACT ("Ref is whatever Redacted would say") without
// reimplementing that rule's internals in the test.
func TestSealFailedError_Error_NeverRevealsPlaintext(t *testing.T) {
	sealErrCause := errors.New("sealer wiped")
	cases := map[string]struct {
		raw         string
		strategy    secret.ResolvedStrategy
		wantRef     string
		mustNotHave string
	}{
		"literal scheme": {
			raw: "literal://" + vaultPlaintext, strategy: secret.StrategyLiteral,
			wantRef: "[redacted]", mustNotHave: vaultPlaintext,
		},
		"bare literal fallback": {
			raw: vaultPlaintext, strategy: secret.StrategyLiteral,
			wantRef: "[redacted]", mustNotHave: vaultPlaintext,
		},
		"resolved credential reference": {
			raw: "credential://ca-key", strategy: secret.StrategyCredential,
			wantRef: "credential://ca-key", mustNotHave: vaultPlaintext,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ref := (&secret.SecretValue{Raw: tc.raw, Strategy: tc.strategy}).Redacted()
			err := &secret.SealFailedError{Ref: ref, Err: sealErrCause}
			got := err.Error()
			if tc.mustNotHave != "" && strings.Contains(got, tc.mustNotHave) {
				t.Fatalf("Error() = %q, must not contain %q", got, tc.mustNotHave)
			}
			if !strings.Contains(got, tc.wantRef) {
				t.Errorf("Error() = %q, want it to contain %q", got, tc.wantRef)
			}
		})
	}
}

// The reflection / %#v / json.Marshal check that used to live here, against
// a hand-constructed *SealFailedError{Ref: ...}, is REMOVED rather than kept
// alongside the real one (go-qrdb#114 review, round 4): a hand-built value
// only ever proves the type CAN be rendered safely once redacted, never that
// NewSecretValue's actual failure path redacts before construction — a
// regression that reintroduced a Raw field and populated it with the
// plaintext would leave a synthetic-only check like that green. The
// authoritative version runs against the REAL error NewSecretValue's own
// sealing-failure path returns, in
// pkg/secret/seal_failure_test.go's runSealFailureChild.

// ---------- default display paths are redacted ----------

func TestSecretValue_FormattingNeverRevealsPlaintext(t *testing.T) {
	cfg := &vaultConfig{Host: "localhost", Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}
	byValue := valueTargetConfig{Password: secret.SecretValue{Raw: vaultScheme + "db-key"}}
	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), &byValue); err != nil {
		t.Fatalf("Process: %v", err)
	}

	rendered := map[string]string{
		"struct %v":       fmt.Sprintf("%v", cfg),
		"struct %+v":      fmt.Sprintf("%+v", cfg),
		"struct %s":       fmt.Sprintf("%s", cfg),
		"value struct %v": fmt.Sprintf("%v", byValue),
		"field %v":        fmt.Sprintf("%v", cfg.Password),
		"field %s":        fmt.Sprintf("%s", cfg.Password),
		"field %q":        fmt.Sprintf("%q", cfg.Password),
		"field %d":        fmt.Sprintf("%d", cfg.Password),
		"Sprint":          fmt.Sprint(cfg.Password),
		"Sprintln":        fmt.Sprintln(cfg.Password),
	}
	for name, out := range rendered {
		if strings.Contains(out, vaultPlaintext) {
			t.Errorf("%s leaked the plaintext: %s", name, out)
		}
		if !strings.Contains(out, vaultScheme+"db-key") {
			t.Errorf("%s should show the reference, got: %s", name, out)
		}
	}
}

func TestSecretValue_FormattingNilPointerIsSafe(t *testing.T) {
	var sv *secret.SecretValue
	if out := fmt.Sprintf("%v", sv); strings.Contains(out, vaultPlaintext) {
		t.Errorf("nil pointer rendered %q", out)
	}
}

func TestSecretValue_StructuredLoggingNeverRevealsPlaintext(t *testing.T) {
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
	if _, err := secret.NewConfigProcessor(newVaultRegistry()).Process(context.Background(), cfg); err != nil {
		t.Fatalf("Process: %v", err)
	}

	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(newHandler(&buf))
			log.Info("boot", "password", cfg.Password, "config", cfg)
			if strings.Contains(buf.String(), vaultPlaintext) {
				t.Errorf("log leaked the plaintext: %s", buf.String())
			}
			if !strings.Contains(buf.String(), vaultScheme+"db-key") {
				t.Errorf("log should show the reference, got: %s", buf.String())
			}
		})
	}
}

func TestSecretValue_FormattingRedactsLiterals(t *testing.T) {
	// A literal's Raw IS the secret, so the display paths must show the marker
	// rather than the reference.
	sv := mustSecretValue(t, "literal://"+vaultPlaintext, []byte(vaultPlaintext), secret.StrategyLiteral)
	for name, out := range map[string]string{
		"%v":       fmt.Sprintf("%v", sv),
		"%q":       fmt.Sprintf("%q", sv),
		"LogValue": sv.LogValue().String(),
	} {
		if strings.Contains(out, vaultPlaintext) {
			t.Errorf("%s leaked a literal's plaintext: %s", name, out)
		}
	}
}

func TestSecretValue_StringStillReturnsPlaintext(t *testing.T) {
	// The deliberate accessor is unchanged: redacting it is a run-time behaviour
	// break reserved for the stack's coordinated release, and this pins the
	// current contract so the change cannot happen by accident.
	sv := mustSecretValue(t, vaultScheme+"db-key", []byte(vaultPlaintext), secret.StrategyCredential)
	if got := sv.String(); got != vaultPlaintext { //nolint:staticcheck // pinning the deprecated accessor's contract
		t.Errorf("String() = %q, want %q", got, vaultPlaintext)
	}
}

// ---------- a resolver that breaks its own contract ----------

// nilResolver claims every "vault://" reference and then reports success with
// no value, which SecretResolver forbids.
type nilResolver struct{}

func (r *nilResolver) Scheme() string { return "vault" }

func (r *nilResolver) CanResolve(raw string) bool { return strings.HasPrefix(raw, vaultScheme) }

func (r *nilResolver) Resolve(_ context.Context, _ string) (*secret.SecretValue, error) {
	return nil, nil
}

func newNilRegistry() *secret.ResolverRegistry {
	reg := secret.NewResolverRegistry()
	reg.Register(&nilResolver{})
	return reg
}

func TestResolverRegistry_RejectsNilSuccess(t *testing.T) {
	sv, err := newNilRegistry().Resolve(context.Background(), vaultScheme+"db-key")
	if sv != nil {
		t.Errorf("SecretValue = %+v, want nil", sv)
	}
	var failed *secret.ResolutionFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want ResolutionFailedError", err)
	}
	if failed.Scheme != "vault" || failed.Raw != vaultScheme+"db-key" {
		t.Errorf("error = %+v, want it to name the scheme and reference", failed)
	}
	if failed.Unwrap() == nil {
		t.Error("Unwrap must expose the contract violation")
	}
}

func TestProcess_NilSuccessIsNotCountedAsResolved(t *testing.T) {
	// Neither target may report success over a resolver that produced nothing —
	// the protected one because adopt would be a silent no-op, the plain one
	// because it would have dereferenced the nil.
	cases := map[string]any{
		"protected target": &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}},
		"plain string":     &mixedConfig{Legacy: vaultScheme + "db-key"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := secret.NewConfigProcessor(newNilRegistry(), secret.WithFailClosed()).
				Process(context.Background(), cfg)
			if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
				t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
			}
			if result.Resolved != 0 {
				t.Errorf("Resolved = %d, want 0", result.Resolved)
			}
			if len(result.Unresolved) != 1 {
				t.Errorf("Unresolved = %v, want exactly one path", result.Unresolved)
			}
			if len(result.Errors) != 1 {
				t.Errorf("Errors = %v, want exactly one", result.Errors)
			}
		})
	}
}

func TestProcess_NilSuccessLeavesProtectedTargetEmpty(t *testing.T) {
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
	if _, err := secret.NewConfigProcessor(newNilRegistry()).Process(context.Background(), cfg); !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
	}
	if cfg.Password.Raw != vaultScheme+"db-key" {
		t.Errorf("Raw = %q, want the reference preserved", cfg.Password.Raw)
	}
	if got := plaintextOf(t, cfg.Password); got != "" {
		t.Errorf("plaintext = %q, want empty", got)
	}
}

// ---------- a protected target fails the call, and never keeps stale material ----------

// flakyResolver resolves normally until it is switched to failing, which models
// a credential being revoked or its backend going down between two Process
// calls over the same config.
type flakyResolver struct{ failing bool }

func (r *flakyResolver) Scheme() string { return "vault" }

func (r *flakyResolver) CanResolve(raw string) bool { return strings.HasPrefix(raw, vaultScheme) }

func (r *flakyResolver) Resolve(ctx context.Context, raw string) (*secret.SecretValue, error) {
	if r.failing {
		return nil, errRefreshFailed
	}
	return secret.NewSecretValue(ctx, raw, []byte(vaultPlaintext), secret.StrategyCredential)
}

var errRefreshFailed = errors.New("backend unavailable")

func TestProcess_FailedRefreshDoesNotLeaveStaleSecretUsable(t *testing.T) {
	resolver := &flakyResolver{}
	reg := secret.NewResolverRegistry()
	reg.Register(resolver)
	proc := secret.NewConfigProcessor(reg)
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}

	if _, err := proc.Process(context.Background(), cfg); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	if got := plaintextOf(t, cfg.Password); got != vaultPlaintext {
		t.Fatalf("first Process: plaintext = %q, want %q", got, vaultPlaintext)
	}

	resolver.failing = true
	result, err := proc.Process(context.Background(), cfg)
	if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Fatalf("failed refresh: err = %v, want ErrUnresolvedSecretURI", err)
	}
	if got := plaintextOf(t, cfg.Password); got != "" {
		t.Errorf("stale secret is still readable after a failed refresh: %q", got)
	}
	buf, err := cfg.Password.Open(t.Context())
	if err == nil && buf != nil {
		defer buf.Destroy()
		t.Errorf("Open still yields a buffer of %d bytes after a failed refresh", buf.Size())
	}
	if len(result.ProtectedUnresolved) != 1 || result.ProtectedUnresolved[0] != "Password" {
		t.Errorf("ProtectedUnresolved = %v, want [Password]", result.ProtectedUnresolved)
	}
}

func TestProcess_ReferenceRemovedClearsThePreviousSecret(t *testing.T) {
	// The same guarantee when the config itself changes: a target whose
	// reference was deleted must not go on serving what it resolved last time.
	proc := secret.NewConfigProcessor(newVaultRegistry())
	cfg := &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
	if _, err := proc.Process(context.Background(), cfg); err != nil {
		t.Fatalf("first Process: %v", err)
	}

	cfg.Password.Raw = ""
	if _, err := proc.Process(context.Background(), cfg); !errors.Is(err, secret.ErrUnresolvedSecretURI) {
		t.Fatalf("err = %v, want ErrUnresolvedSecretURI", err)
	}
	if got := plaintextOf(t, cfg.Password); got != "" {
		t.Errorf("previous secret survived the reference being removed: %q", got)
	}
}

func TestProcess_ProtectedFailureIsAnErrorWithoutFailClosed(t *testing.T) {
	// The protected target fails the call on default options. A caller reading
	// only err — the normal Go contract — must not be told the config is ready.
	cases := map[string]func() (*secret.ResolverRegistry, *vaultConfig){
		"resolver error": func() (*secret.ResolverRegistry, *vaultConfig) {
			reg := secret.NewResolverRegistry()
			reg.Register(&vaultResolver{err: errRefreshFailed})
			return reg, &vaultConfig{Password: &secret.SecretValue{Raw: vaultScheme + "db-key"}}
		},
		"nothing to resolve": func() (*secret.ResolverRegistry, *vaultConfig) {
			return newVaultRegistry(), &vaultConfig{Password: &secret.SecretValue{}}
		},
		"inline secret": func() (*secret.ResolverRegistry, *vaultConfig) {
			return newVaultRegistry(), &vaultConfig{Password: &secret.SecretValue{Raw: "literal://" + vaultPlaintext}}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			reg, cfg := build()
			result, err := secret.NewConfigProcessor(reg).Process(context.Background(), cfg)
			if !errors.Is(err, secret.ErrUnresolvedSecretURI) {
				t.Fatalf("err = %v, want ErrUnresolvedSecretURI on default options", err)
			}
			if len(result.ProtectedUnresolved) != 1 || result.ProtectedUnresolved[0] != "Password" {
				t.Errorf("ProtectedUnresolved = %v, want [Password]", result.ProtectedUnresolved)
			}
			for _, path := range result.ProtectedUnresolved {
				if !contains(result.Unresolved, path) {
					t.Errorf("%q in ProtectedUnresolved but not in Unresolved", path)
				}
			}
		})
	}
}

func TestProcess_PlainStringFailureStillNeedsFailClosed(t *testing.T) {
	// The deprecated string path keeps its old contract exactly: a failure is a
	// diagnostic the caller opts into, not an error by default.
	reg := secret.NewResolverRegistry()
	reg.Register(&vaultResolver{err: errRefreshFailed})
	cfg := &mixedConfig{Legacy: vaultScheme + "db-key"}

	result, err := secret.NewConfigProcessor(reg).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("err = %v, want nil — the legacy contract is unchanged", err)
	}
	if len(result.Unresolved) != 1 {
		t.Errorf("Unresolved = %v, want the failure reported", result.Unresolved)
	}
	if len(result.ProtectedUnresolved) != 0 {
		t.Errorf("ProtectedUnresolved = %v, want empty for a string target", result.ProtectedUnresolved)
	}
}

// contains reports whether paths holds want.
func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// TestSecretValue_UnclaimedSchemeIsNeverDisplayed drives the leak Codex found:
// a value no resolver claims comes back as a literal whose Raw IS the secret,
// even though it is shaped like a reference. Nothing that renders it by default
// may show it.
func TestSecretValue_UnclaimedSchemeIsNeverDisplayed(t *testing.T) {
	// A registry with a resolver that claims something else entirely, so these
	// values fall through to the literal fallback exactly as they would in a
	// deployment missing the resolver for a scheme its config uses.
	reg := newVaultRegistry()

	for name, raw := range map[string]string{
		"unknown scheme":     "unknown://" + vaultPlaintext,
		"url with password":  "https://user:" + vaultPlaintext + "@example.test",
		"registered-looking": "kms://" + vaultPlaintext,
	} {
		t.Run(name, func(t *testing.T) {
			sv, err := reg.Resolve(context.Background(), raw)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if sv.Strategy != secret.StrategyLiteral {
				t.Fatalf("Strategy = %q, want the literal fallback", sv.Strategy)
			}
			var logged bytes.Buffer
			slog.New(slog.NewJSONHandler(&logged, nil)).Info("boot", "secret", sv)

			for path, out := range map[string]string{
				"Redacted": sv.Redacted(),
				"%v":       fmt.Sprintf("%v", sv),
				"%s":       fmt.Sprintf("%s", sv),
				"%q":       fmt.Sprintf("%q", sv),
				"Sprint":   fmt.Sprint(sv),
				"LogValue": sv.LogValue().String(),
				"slog":     logged.String(),
			} {
				if strings.Contains(out, vaultPlaintext) {
					t.Errorf("%s leaked an unclaimed-scheme literal: %s", path, out)
				}
			}
		})
	}
}
