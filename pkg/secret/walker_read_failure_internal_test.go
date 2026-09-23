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

package secret

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// unreadableScheme marks references unreadableResolver claims.
const unreadableScheme = "unreadable://"

// unreadableResolver resolves successfully to a SecretValue whose sealed
// secret has been destroyed underneath it — the state a shutdown WipeAll or a
// racing Destroy leaves — so reading the value back fails with a real
// secure.Secret.Open error rather than a simulated one. It is an internal test
// because only this package can reach the SecretValue's sealed field.
type unreadableResolver struct{}

func (unreadableResolver) CanResolve(raw string) bool {
	return strings.HasPrefix(raw, unreadableScheme)
}

func (unreadableResolver) Scheme() string { return "unreadable" }

func (unreadableResolver) Resolve(_ context.Context, raw string) (*SecretValue, error) {
	sv, err := NewSecretValue(raw, []byte("resolved-but-unreadable"), ResolvedStrategy("unreadable"))
	if err != nil {
		return nil, err
	}
	sv.secret.Destroy() // sealed value gone; the pointer stays, as after a sweep
	return sv, nil
}

// TestProcess_UnreadableResolvedStringFailsClosed pins the walker's plain
// string paths — a struct field and a map value — to fail CLOSED when a
// reference resolves but the resolved value cannot be read back: the field
// keeps its reference, the path is Unresolved, the failure is a
// *ResolvedReadError wrapping the real cause, and nothing counts as resolved.
// The earlier form wrote SecretValue.String()'s "" into the field and counted
// it resolved, erasing the reference in favour of an empty credential.
func TestProcess_UnreadableResolvedStringFailsClosed(t *testing.T) {
	type config struct {
		Password string
		Extra    map[string]string
	}
	ref := unreadableScheme + "db-password"
	cfg := &config{Password: ref, Extra: map[string]string{"token": ref}}

	reg := NewResolverRegistry()
	reg.Register(unreadableResolver{})
	result, err := NewConfigProcessor(reg).Process(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if cfg.Password != ref {
		t.Errorf("Password = %q, want the reference %q left in place", cfg.Password, ref)
	}
	if got := cfg.Extra["token"]; got != ref {
		t.Errorf("Extra[token] = %q, want the reference %q left in place", got, ref)
	}
	if result.Resolved != 0 {
		t.Errorf("Resolved = %d, want 0: an unreadable value is not a resolution", result.Resolved)
	}
	for _, path := range []string{"Password", "Extra[token]"} {
		found := false
		for _, u := range result.Unresolved {
			found = found || u == path
		}
		if !found {
			t.Errorf("Unresolved = %v, want it to contain %q", result.Unresolved, path)
		}
	}
	readErrs := 0
	for _, e := range result.Errors {
		var re *ResolvedReadError
		if !errors.As(e, &re) {
			t.Errorf("recorded error %v (%T), want *ResolvedReadError", e, e)
			continue
		}
		readErrs++
		if msg := re.Error(); !strings.Contains(msg, re.Path) || !strings.Contains(msg, "could not be read") {
			t.Errorf("ResolvedReadError.Error() = %q, want it to name the path and the failure", msg)
		}
		if strings.Contains(re.Error(), "resolved-but-unreadable") {
			t.Errorf("ResolvedReadError.Error() leaks the resolved value: %q", re.Error())
		}
		var nilSecret *secure.NilSecretError
		if !errors.As(e, &nilSecret) {
			t.Errorf("ResolvedReadError %v does not wrap the real Secret.Open failure", e)
		}
	}
	if readErrs != 2 {
		t.Errorf("recorded %d *ResolvedReadError, want 2", readErrs)
	}

	// Fail-closed mode turns the recorded failure into Process's own error.
	cfg2 := &config{Password: ref}
	if _, err := NewConfigProcessor(reg, WithFailClosed()).Process(context.Background(), cfg2); !errors.Is(err, ErrUnresolvedSecretURI) {
		t.Errorf("fail-closed Process err = %v, want ErrUnresolvedSecretURI", err)
	}
}

// TestSecretValue_ReadString_DistinguishesEmptyFromUnreadable pins readString's
// contract, which the walker's fail-closed behaviour depends on.
func TestSecretValue_ReadString_DistinguishesEmptyFromUnreadable(t *testing.T) {
	empty, err := NewSecretValue("x://empty", nil, ResolvedStrategy("x"))
	if err != nil {
		t.Fatalf("NewSecretValue(empty): %v", err)
	}
	if s, err := empty.readString(); s != "" || err != nil {
		t.Errorf("readString(empty) = (%q, %v), want (\"\", nil)", s, err)
	}

	full, err := NewSecretValue("x://full", []byte("value"), ResolvedStrategy("x"))
	if err != nil {
		t.Fatalf("NewSecretValue(full): %v", err)
	}
	if s, err := full.readString(); s != "value" || err != nil {
		t.Errorf("readString(full) = (%q, %v), want (\"value\", nil)", s, err)
	}
	full.secret.Destroy()
	if s, err := full.readString(); s != "" || err == nil {
		t.Errorf("readString(destroyed) = (%q, %v), want an error", s, err)
	}
}
