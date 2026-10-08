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

package service

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/dek/dektest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func newDeviceDEKService(t *testing.T) (*DEKService, *dektest.Device) {
	t.Helper()
	device := dektest.New()
	svc, err := NewDEKService(device)
	if err != nil {
		t.Fatal(err)
	}
	return svc, device
}

// TestDEKServiceOverADevice runs the whole DEK lifecycle against a device that
// never exports a key: every operation is served by the device and the caller
// only ever holds opaque ciphertext handles.
func TestDEKServiceOverADevice(t *testing.T) {
	svc, device := newDeviceDEKService(t)
	ctx := asTenant("acme")

	sealed, err := svc.Encrypt(ctx, "", []byte("v"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !bytes.HasPrefix(sealed.Ciphertext, []byte("device-handle:")) {
		t.Fatalf("ciphertext %q is not a device handle", sealed.Ciphertext)
	}
	opened, err := svc.Decrypt(ctx, "", sealed.Ciphertext, []byte("aad"))
	if err != nil || !bytes.Equal(opened.Plaintext, []byte("v")) {
		t.Fatalf("Decrypt = %+v, %v", opened, err)
	}
	if _, err := svc.Rotate(ctx, ""); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	rw, err := svc.Rewrap(ctx, "", sealed.Ciphertext, []byte("aad"))
	if err != nil || rw.FromVersion != 1 || rw.ToVersion != 2 {
		t.Fatalf("Rewrap = %+v, %v", rw, err)
	}
	if _, err := svc.Retire(ctx, "", 2); !errors.Is(err, dek.ErrRetireCurrentVersion) {
		t.Fatalf("retire current: err = %v", err)
	}
	st, err := svc.Retire(ctx, "", 1)
	if err != nil || st.Versions[0].RetiredAt == nil {
		t.Fatalf("Retire = %+v, %v", st, err)
	}
	if _, err := svc.Decrypt(ctx, "", sealed.Ciphertext, []byte("aad")); err == nil {
		t.Fatal("a retired version's ciphertext still opens")
	}

	want := []string{"encrypt:acme", "decrypt:acme", "rotate:acme", "rewrap:acme", "retire:acme", "retire:acme", "decrypt:acme"}
	got := device.Ops()
	if len(got) != len(want) {
		t.Fatalf("device ops = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("device ops = %v, want %v", got, want)
		}
	}
}

// TestDEKServiceOverADeviceKeepsTenantIsolation: the service's tenant binding is
// enforced above the provider, so a device cannot be asked to cross tenants.
func TestDEKServiceOverADeviceKeepsTenantIsolation(t *testing.T) {
	svc, device := newDeviceDEKService(t)
	if _, err := svc.Encrypt(asTenant("acme"), "", []byte("v"), nil); err != nil {
		t.Fatal(err)
	}
	before := len(device.Ops())
	for _, caller := range []string{"globex", tenantid.System} {
		_, err := svc.Status(asTenant(caller), "acme")
		var ct *dek.CrossTenantError
		if !errors.As(err, &ct) {
			t.Fatalf("%s: err = %v, want *dek.CrossTenantError", caller, err)
		}
	}
	if after := len(device.Ops()); after != before {
		t.Fatalf("a refused cross-tenant call reached the device (%d ops, was %d)", after, before)
	}
}
