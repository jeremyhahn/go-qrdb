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

package tenant

import (
	"bytes"
	"testing"
)

func TestPrefixKeyNormal(t *testing.T) {
	got := PrefixKey("tenant-1", "my-key")
	want := "tenant-1/my-key"
	if got != want {
		t.Errorf("PrefixKey() = %q, want %q", got, want)
	}
}

func TestPrefixKeyWithSystemTenant(t *testing.T) {
	got := PrefixKey(SystemTenantID, "some-key")
	want := "system/some-key"
	if got != want {
		t.Errorf("PrefixKey() = %q, want %q", got, want)
	}
}

func TestPrefixKeyEmptyKey(t *testing.T) {
	got := PrefixKey("tenant-1", "")
	want := "tenant-1/"
	if got != want {
		t.Errorf("PrefixKey() = %q, want %q", got, want)
	}
}

func TestPrefixKeyBytesNormal(t *testing.T) {
	got := PrefixKeyBytes("tenant-1", []byte("my-key"))
	want := []byte("tenant-1/my-key")
	if !bytes.Equal(got, want) {
		t.Errorf("PrefixKeyBytes() = %q, want %q", got, want)
	}
}

func TestPrefixKeyBytesEmptyKey(t *testing.T) {
	got := PrefixKeyBytes("tenant-1", []byte{})
	want := []byte("tenant-1/")
	if !bytes.Equal(got, want) {
		t.Errorf("PrefixKeyBytes() = %q, want %q", got, want)
	}
}

func TestPrefixKeyBytesWithSystemTenant(t *testing.T) {
	got := PrefixKeyBytes(SystemTenantID, []byte("data"))
	want := []byte("system/data")
	if !bytes.Equal(got, want) {
		t.Errorf("PrefixKeyBytes() = %q, want %q", got, want)
	}
}

func TestStripPrefixNormal(t *testing.T) {
	got := StripPrefix("tenant-1", "tenant-1/my-key")
	want := "my-key"
	if got != want {
		t.Errorf("StripPrefix() = %q, want %q", got, want)
	}
}

func TestStripPrefixNoPrefixReturnsOriginal(t *testing.T) {
	got := StripPrefix("tenant-1", "other-tenant/my-key")
	want := "other-tenant/my-key"
	if got != want {
		t.Errorf("StripPrefix() = %q, want %q", got, want)
	}
}

func TestStripPrefixKeyEqualsPrefix(t *testing.T) {
	// Key is exactly the prefix (no trailing content after separator).
	got := StripPrefix("tenant-1", "tenant-1/")
	want := "tenant-1/"
	if got != want {
		t.Errorf("StripPrefix() = %q, want %q", got, want)
	}
}

func TestStripPrefixEmptyKey(t *testing.T) {
	got := StripPrefix("tenant-1", "")
	want := ""
	if got != want {
		t.Errorf("StripPrefix() = %q, want %q", got, want)
	}
}

func TestStripPrefixBytesNormal(t *testing.T) {
	got := StripPrefixBytes("tenant-1", []byte("tenant-1/my-key"))
	want := []byte("my-key")
	if !bytes.Equal(got, want) {
		t.Errorf("StripPrefixBytes() = %q, want %q", got, want)
	}
}

func TestStripPrefixBytesNoPrefixReturnsOriginal(t *testing.T) {
	original := []byte("other-tenant/my-key")
	got := StripPrefixBytes("tenant-1", original)
	if !bytes.Equal(got, original) {
		t.Errorf("StripPrefixBytes() = %q, want %q", got, original)
	}
}

func TestStripPrefixBytesEmptyKey(t *testing.T) {
	got := StripPrefixBytes("tenant-1", []byte{})
	if !bytes.Equal(got, []byte{}) {
		t.Errorf("StripPrefixBytes() = %q, want empty", got)
	}
}

func TestStripPrefixBytesKeyEqualsPrefix(t *testing.T) {
	input := []byte("tenant-1/")
	got := StripPrefixBytes("tenant-1", input)
	if !bytes.Equal(got, input) {
		t.Errorf("StripPrefixBytes() = %q, want %q", got, input)
	}
}

func TestTenantPrefix(t *testing.T) {
	got := TenantPrefix("tenant-1")
	want := "tenant-1/"
	if got != want {
		t.Errorf("TenantPrefix() = %q, want %q", got, want)
	}
}

func TestTenantPrefixSystemTenant(t *testing.T) {
	got := TenantPrefix(SystemTenantID)
	want := "system/"
	if got != want {
		t.Errorf("TenantPrefix() = %q, want %q", got, want)
	}
}
