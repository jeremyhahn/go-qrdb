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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestUserBinding_SurfaceMatchesTypeScriptSDK reads the TypeScript SDK's
// Wails client (sdk/typescript/src/wails.ts) and asserts that every method
// it calls on main.UserBinding exists on the Go UserBinding with the same
// number of positional arguments. The GUI's browser mock implements
// whatever the SDK calls, so only this check catches a method the SDK
// calls that the desktop binary does not export.
func TestUserBinding_SurfaceMatchesTypeScriptSDK(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("..", "..", "sdk", "typescript", "src", "wails.ts"))
	if err != nil {
		t.Fatalf("read wails.ts: %v", err)
	}
	text := string(src)
	const anchor = "getBindings().main.UserBinding)"
	start := strings.Index(text, anchor)
	if start < 0 {
		t.Fatalf("wails.ts no longer wraps main.UserBinding (%q not found)", anchor)
	}
	block := text[start:]
	if end := strings.Index(block, "/** @inheritdoc */"); end > 0 {
		block = block[:end]
	}

	call := regexp.MustCompile(`\bb\.([A-Z]\w*)\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	calls := map[string]int{}
	for _, m := range call.FindAllStringSubmatch(block, -1) {
		args := strings.TrimSpace(m[2])
		n := 0
		if args != "" {
			n = len(strings.Split(args, ","))
		}
		calls[m[1]] = n
	}
	if len(calls) == 0 {
		t.Fatal("found no UserBinding calls in wails.ts")
	}

	typ := reflect.TypeOf(&UserBinding{})
	names := make([]string, 0, len(calls))
	for name := range calls {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m, ok := typ.MethodByName(name)
		if !ok {
			t.Errorf("wails.ts calls UserBinding.%s, which the Go UserBinding does not export", name)
			continue
		}
		// NumIn counts the receiver.
		if got, want := m.Type.NumIn()-1, calls[name]; got != want {
			t.Errorf("UserBinding.%s takes %d arguments; wails.ts passes %d", name, got, want)
		}
	}
	for _, name := range []string{"GetByCertThumbprint", "AddCertBinding", "RemoveCertBinding"} {
		if _, ok := calls[name]; !ok {
			t.Errorf("expected wails.ts to call UserBinding.%s", name)
		}
	}
}

func TestUserBinding_GetByCertThumbprint_Success(t *testing.T) {
	var gotThumb string
	svc := &mockUserService{
		byThumbFn: func(_ context.Context, thumbprint string) (*user.User, error) {
			gotThumb = thumbprint
			return &user.User{ID: 1001, Username: "alice", TenantID: "system", Lifecycle: user.LifecycleActive,
				CertBindings: []user.CertBinding{{Thumbprint: thumbprint}}}, nil
		},
	}
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, svc, nil, nil))

	u, err := b.GetByCertThumbprint("tp-abc")
	if err != nil {
		t.Fatalf("GetByCertThumbprint: %v", err)
	}
	if gotThumb != "tp-abc" {
		t.Fatalf("service saw thumbprint %q, want tp-abc", gotThumb)
	}
	if u == nil || u.Username != "alice" {
		t.Fatalf("unexpected user: %+v", u)
	}
}

func TestUserBinding_GetByCertThumbprint_ServiceError(t *testing.T) {
	svc := &mockUserService{
		byThumbFn: func(_ context.Context, _ string) (*user.User, error) {
			return nil, errors.New("no user bound")
		},
	}
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, svc, nil, nil))
	if _, err := b.GetByCertThumbprint("tp-missing"); err == nil {
		t.Fatal("expected error from GetByCertThumbprint")
	}
}

func TestUserBinding_AddCertBinding_Success(t *testing.T) {
	var (
		gotID    uint64
		gotThumb string
		gotNA    int64
	)
	svc := &mockUserService{
		addCertFn: func(ctx context.Context, id uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
			gotID, gotThumb, gotNA = id, thumbprint, notAfterUnix
			return (&mockUserService{}).AddCertBinding(ctx, id, thumbprint, notAfterUnix)
		},
	}
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, svc, nil, nil))

	u, err := b.AddCertBinding("1001", "tp-new", 1900000000)
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if gotID != 1001 || gotThumb != "tp-new" || gotNA != 1900000000 {
		t.Fatalf("service saw (%d, %q, %d)", gotID, gotThumb, gotNA)
	}
	if u == nil || len(u.CertBindings) != 1 || u.CertBindings[0].Thumbprint != "tp-new" {
		t.Fatalf("expected the new binding on the returned user, got %+v", u)
	}
}

func TestUserBinding_AddCertBinding_InvalidID(t *testing.T) {
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil))
	if _, err := b.AddCertBinding("not-a-number", "tp", 0); err == nil {
		t.Fatal("expected error for a non-numeric user id")
	}
}

func TestUserBinding_AddCertBinding_ServiceError(t *testing.T) {
	svc := &mockUserService{
		addCertFn: func(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
			return nil, errors.New("thumbprint already bound")
		},
	}
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, svc, nil, nil))
	if _, err := b.AddCertBinding("1001", "tp", 0); err == nil {
		t.Fatal("expected error from AddCertBinding")
	}
}

func TestUserBinding_RemoveCertBinding_Success(t *testing.T) {
	var (
		gotID    uint64
		gotThumb string
	)
	svc := &mockUserService{
		removeCertFn: func(_ context.Context, id uint64, thumbprint string) (*user.User, error) {
			gotID, gotThumb = id, thumbprint
			return &user.User{ID: id, Username: "testuser", TenantID: "system", Lifecycle: user.LifecycleActive}, nil
		},
	}
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, svc, nil, nil))

	u, err := b.RemoveCertBinding("1001", "tp-old")
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if gotID != 1001 || gotThumb != "tp-old" {
		t.Fatalf("service saw (%d, %q)", gotID, gotThumb)
	}
	if u == nil || len(u.CertBindings) != 0 {
		t.Fatalf("expected no bindings on the returned user, got %+v", u)
	}
}

func TestUserBinding_RemoveCertBinding_InvalidID(t *testing.T) {
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, &mockUserService{}, nil, nil))
	if _, err := b.RemoveCertBinding("", "tp"); err == nil {
		t.Fatal("expected error for an empty user id")
	}
}

func TestUserBinding_RemoveCertBinding_ServiceError(t *testing.T) {
	svc := &mockUserService{
		removeCertFn: func(_ context.Context, _ uint64, _ string) (*user.User, error) {
			return nil, errors.New("no such binding")
		},
	}
	b := NewUserBinding(testLogger(t), newAuthTestConnectionBinding(t, svc, nil, nil))
	if _, err := b.RemoveCertBinding("1001", "tp"); err == nil {
		t.Fatal("expected error from RemoveCertBinding")
	}
}

func TestUserBinding_CertBindingVerbs_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetByCertThumbprint("tp")
	assertNotConnected(t, err)
	_, err = b.AddCertBinding("1", "tp", 0)
	assertNotConnected(t, err)
	_, err = b.RemoveCertBinding("1", "tp")
	assertNotConnected(t, err)
}
