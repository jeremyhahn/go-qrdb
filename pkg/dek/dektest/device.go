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

// Package dektest provides a fake device-backed dek.Provider for tests.
package dektest

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
)

// handlePrefix marks a ciphertext this device issued.
const handlePrefix = "device-handle:"

// Device is a fake of a hardware-backed dek.Provider. It models a device that
// holds every tenant's key versions internally: the ciphertext a caller gets
// back is an opaque handle the device resolves, and no key bytes ever cross
// the dek.Provider interface -- which is what the fake proves the interface
// needs none of. It keeps the semantics a Provider promises: tenant and
// associated-data binding, rotation, rewrap without exposing plaintext, and
// irreversible retirement.
//
// It is safe for concurrent use.
type Device struct {
	mu      sync.Mutex
	tenants map[string]*tenantRing
	sealed  map[string]sealedEntry
	next    uint64
	ops     []string
}

type tenantRing struct {
	current  uint64
	created  map[uint64]time.Time
	retired  map[uint64]time.Time
	versions []uint64
}

type sealedEntry struct {
	tenant    string
	version   uint64
	aad       []byte
	plaintext []byte
}

// Compile-time check that the fake satisfies the seam.
var _ dek.Provider = (*Device)(nil)

// New returns an empty Device.
func New() *Device {
	return &Device{tenants: map[string]*tenantRing{}, sealed: map[string]sealedEntry{}}
}

// Ops returns the operations the device served, in order, as "<op>:<tenant>".
// A test asserts through it that a request reached the device.
func (d *Device) Ops() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

// ring returns the tenant's keyring, creating version 1 on first use. The
// caller holds d.mu.
func (d *Device) ring(tenantID string) *tenantRing {
	r, ok := d.tenants[tenantID]
	if !ok {
		r = &tenantRing{created: map[uint64]time.Time{}, retired: map[uint64]time.Time{}}
		d.tenants[tenantID] = r
		d.addVersion(r)
	}
	return r
}

func (d *Device) addVersion(r *tenantRing) {
	r.current++
	r.versions = append(r.versions, r.current)
	r.created[r.current] = time.Now()
}

func (d *Device) status(tenantID string) *dek.Status {
	st := &dek.Status{TenantID: tenantID, Algorithm: "device"}
	r, ok := d.tenants[tenantID]
	if !ok {
		return st
	}
	st.Current = r.current
	for _, v := range r.versions {
		info := dek.VersionInfo{Version: v, CreatedAt: r.created[v]}
		if at, gone := r.retired[v]; gone {
			at := at
			info.RetiredAt = &at
		} else {
			info.ProviderID = "device"
			info.KeyRef = tenantID
		}
		st.Versions = append(st.Versions, info)
	}
	return st
}

// Status implements dek.Provider.
func (d *Device) Status(_ context.Context, tenantID string) (*dek.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, "status:"+tenantID)
	return d.status(tenantID), nil
}

// Rotate implements dek.Provider.
func (d *Device) Rotate(_ context.Context, tenantID string) (*dek.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, "rotate:"+tenantID)
	if r, ok := d.tenants[tenantID]; ok {
		d.addVersion(r)
	} else {
		d.ring(tenantID)
	}
	return d.status(tenantID), nil
}

// Retire implements dek.Provider.
func (d *Device) Retire(_ context.Context, tenantID string, version uint64) (*dek.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, "retire:"+tenantID)
	r := d.ring(tenantID)
	if version == r.current {
		return nil, &dek.RetireCurrentVersionError{TenantID: tenantID, Version: version}
	}
	if _, ok := r.created[version]; !ok {
		return nil, &dek.UnknownVersionError{TenantID: tenantID, Version: version}
	}
	if _, gone := r.retired[version]; !gone {
		r.retired[version] = time.Now()
		for h, e := range d.sealed {
			if e.tenant == tenantID && e.version == version {
				delete(d.sealed, h)
			}
		}
	}
	return d.status(tenantID), nil
}

// Encrypt implements dek.Provider.
func (d *Device) Encrypt(_ context.Context, tenantID string, plaintext, aad []byte) (*dek.Sealed, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, "encrypt:"+tenantID)
	r := d.ring(tenantID)
	return &dek.Sealed{Ciphertext: d.seal(tenantID, r.current, plaintext, aad), Version: r.current}, nil
}

// seal stores plaintext on the device and returns its handle. The caller holds
// d.mu.
func (d *Device) seal(tenantID string, version uint64, plaintext, aad []byte) []byte {
	d.next++
	handle := handlePrefix + strconv.FormatUint(d.next, 10)
	d.sealed[handle] = sealedEntry{
		tenant:    tenantID,
		version:   version,
		aad:       append([]byte(nil), aad...),
		plaintext: append([]byte(nil), plaintext...),
	}
	return []byte(handle)
}

// open resolves a handle for tenantID and aad. The caller holds d.mu.
func (d *Device) open(tenantID string, ciphertext, aad []byte) (sealedEntry, error) {
	handle := string(ciphertext)
	if !strings.HasPrefix(handle, handlePrefix) {
		return sealedEntry{}, &dek.MalformedCiphertextError{Reason: "not a device ciphertext"}
	}
	e, ok := d.sealed[handle]
	if !ok {
		return sealedEntry{}, &dek.AuthenticationFailedError{TenantID: tenantID}
	}
	if e.tenant != tenantID || !bytes.Equal(e.aad, aad) {
		return sealedEntry{}, &dek.AuthenticationFailedError{TenantID: tenantID, Version: e.version}
	}
	return e, nil
}

// Decrypt implements dek.Provider.
func (d *Device) Decrypt(_ context.Context, tenantID string, ciphertext, aad []byte) (*dek.Opened, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, "decrypt:"+tenantID)
	e, err := d.open(tenantID, ciphertext, aad)
	if err != nil {
		return nil, err
	}
	return &dek.Opened{Plaintext: append([]byte(nil), e.plaintext...), Version: e.version}, nil
}

// Rewrap implements dek.Provider.
func (d *Device) Rewrap(_ context.Context, tenantID string, ciphertext, aad []byte) (*dek.Rewrapped, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, "rewrap:"+tenantID)
	e, err := d.open(tenantID, ciphertext, aad)
	if err != nil {
		return nil, err
	}
	r := d.ring(tenantID)
	// The plaintext moves from one device slot to another and never leaves it.
	out := d.seal(tenantID, r.current, e.plaintext, aad)
	return &dek.Rewrapped{Ciphertext: out, FromVersion: e.version, ToVersion: r.current}, nil
}
