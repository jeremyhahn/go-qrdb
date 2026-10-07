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

package database

import (
	"errors"
	"io"
	"testing"
)

func TestInvalidNameError_Message(t *testing.T) {
	tests := []struct {
		name    string
		errName string
		want    string
	}{
		{"empty", "", `database: invalid database name: ""`},
		{"with_dots", "my.db", `database: invalid database name: "my.db"`},
		{"with_spaces", "my db", `database: invalid database name: "my db"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidNameError{Name: tt.errName}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInvalidNameError_TypeAssertion(t *testing.T) {
	var err error = &InvalidNameError{Name: "bad.name"}
	var target *InvalidNameError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *InvalidNameError")
	}
	if target.Name != "bad.name" {
		t.Errorf("Name = %q, want %q", target.Name, "bad.name")
	}
}

func TestLegacyMetadataError_Message(t *testing.T) {
	err := &LegacyMetadataError{Path: "/data/db/meta.json", Err: errors.New(`json: unknown field "engine"`)}
	want := `database: unsupported metadata format "/data/db/meta.json": json: unknown field "engine"; recreate the database`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestLegacyMetadataError_TypeAssertion(t *testing.T) {
	cause := errors.New("boom")
	var err error = &LegacyMetadataError{Path: "/data/db/meta.json", Err: cause}
	var target *LegacyMetadataError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *LegacyMetadataError")
	}
	if !errors.Is(err, cause) {
		t.Error("Unwrap() did not expose the underlying cause")
	}
}

func TestInvalidBackendError_Message(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		want    string
	}{
		{"empty", "", `database: invalid backend type: ""`},
		{"unknown", "sqlite", `database: invalid backend type: "sqlite"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidBackendError{Backend: tt.backend}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInvalidBackendError_TypeAssertion(t *testing.T) {
	var err error = &InvalidBackendError{Backend: "sqlite"}
	var target *InvalidBackendError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *InvalidBackendError")
	}
	if target.Backend != "sqlite" {
		t.Errorf("Backend = %q, want %q", target.Backend, "sqlite")
	}
}

func TestAlreadyExistsError_Message(t *testing.T) {
	tests := []struct {
		name   string
		dbName string
		want   string
	}{
		{"simple", "mydb", `database: database already exists: "mydb"`},
		{"with_hyphens", "my-db", `database: database already exists: "my-db"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &AlreadyExistsError{Name: tt.dbName}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAlreadyExistsError_TypeAssertion(t *testing.T) {
	var err error = &AlreadyExistsError{Name: "testdb"}
	var target *AlreadyExistsError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *AlreadyExistsError")
	}
	if target.Name != "testdb" {
		t.Errorf("Name = %q, want %q", target.Name, "testdb")
	}
}

func TestNotFoundError_Message(t *testing.T) {
	tests := []struct {
		name   string
		dbName string
		want   string
	}{
		{"simple", "mydb", `database: database not found: "mydb"`},
		{"with_underscores", "my_db", `database: database not found: "my_db"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &NotFoundError{Name: tt.dbName}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNotFoundError_TypeAssertion(t *testing.T) {
	var err error = &NotFoundError{Name: "missing"}
	var target *NotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *NotFoundError")
	}
	if target.Name != "missing" {
		t.Errorf("Name = %q, want %q", target.Name, "missing")
	}
}

func TestNotOpenError_Message(t *testing.T) {
	tests := []struct {
		name   string
		dbName string
		want   string
	}{
		{"simple", "mydb", `database: database not open: "mydb"`},
		{"complex_name", "prod-db-01", `database: database not open: "prod-db-01"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &NotOpenError{Name: tt.dbName}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNotOpenError_TypeAssertion(t *testing.T) {
	var err error = &NotOpenError{Name: "closeddb"}
	var target *NotOpenError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *NotOpenError")
	}
	if target.Name != "closeddb" {
		t.Errorf("Name = %q, want %q", target.Name, "closeddb")
	}
}

func TestOpenError_Message(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	err := &OpenError{Name: "mydb", Err: cause}
	want := `database: failed to open database "mydb": unexpected EOF`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestOpenError_Unwrap(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	err := &OpenError{Name: "mydb", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *OpenError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *OpenError")
	}
	if target.Name != "mydb" {
		t.Errorf("Name = %q, want %q", target.Name, "mydb")
	}
}

func TestCloseError_Message(t *testing.T) {
	cause := io.ErrClosedPipe
	err := &CloseError{Name: "mydb", Err: cause}
	want := `database: failed to close database "mydb": io: read/write on closed pipe`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestCloseError_Unwrap(t *testing.T) {
	cause := io.ErrClosedPipe
	err := &CloseError{Name: "mydb", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *CloseError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *CloseError")
	}
	if target.Name != "mydb" {
		t.Errorf("Name = %q, want %q", target.Name, "mydb")
	}
}

func TestMetaReadError_Message(t *testing.T) {
	cause := io.EOF
	err := &MetaReadError{Path: "/data/mydb/meta.json", Err: cause}
	want := `database: failed to read database metadata "/data/mydb/meta.json": EOF`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestMetaReadError_Unwrap(t *testing.T) {
	cause := io.EOF
	err := &MetaReadError{Path: "/data/mydb/meta.json", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, io.EOF) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *MetaReadError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *MetaReadError")
	}
	if target.Path != "/data/mydb/meta.json" {
		t.Errorf("Path = %q, want %q", target.Path, "/data/mydb/meta.json")
	}
}

func TestMetaWriteError_Message(t *testing.T) {
	cause := io.ErrShortWrite
	err := &MetaWriteError{Path: "/data/mydb/meta.json", Err: cause}
	want := `database: failed to write database metadata "/data/mydb/meta.json": short write`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestMetaWriteError_Unwrap(t *testing.T) {
	cause := io.ErrShortWrite
	err := &MetaWriteError{Path: "/data/mydb/meta.json", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *MetaWriteError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *MetaWriteError")
	}
	if target.Path != "/data/mydb/meta.json" {
		t.Errorf("Path = %q, want %q", target.Path, "/data/mydb/meta.json")
	}
}

func TestDropError_Message(t *testing.T) {
	cause := errors.New("permission denied")
	err := &DropError{Name: "mydb", Err: cause}
	want := `database: failed to drop database "mydb": permission denied`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDropError_Unwrap(t *testing.T) {
	cause := errors.New("permission denied")
	err := &DropError{Name: "mydb", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	var target *DropError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DropError")
	}
	if target.Name != "mydb" {
		t.Errorf("Name = %q, want %q", target.Name, "mydb")
	}
}

func TestStorageCreateError_Message(t *testing.T) {
	cause := errors.New("disk full")
	err := &StorageCreateError{Name: "mydb", Backend: "pebble", Err: cause}
	want := `database: failed to create pebble storage for database "mydb": disk full`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestStorageCreateError_Unwrap(t *testing.T) {
	cause := errors.New("disk full")
	err := &StorageCreateError{Name: "mydb", Backend: "pebble", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	var target *StorageCreateError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *StorageCreateError")
	}
	if target.Name != "mydb" {
		t.Errorf("Name = %q, want %q", target.Name, "mydb")
	}
	if target.Backend != "pebble" {
		t.Errorf("Backend = %q, want %q", target.Backend, "pebble")
	}
}

func TestInvalidModeError_Message(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{"empty", "", `database: invalid database mode: ""`},
		{"unknown", "clustered", `database: invalid database mode: "clustered"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidModeError{Mode: tt.mode}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInvalidModeError_TypeAssertion(t *testing.T) {
	var err error = &InvalidModeError{Mode: "clustered"}
	var target *InvalidModeError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *InvalidModeError")
	}
	if target.Mode != "clustered" {
		t.Errorf("Mode = %q, want %q", target.Mode, "clustered")
	}
}

func TestConnectionRequiredError_Message(t *testing.T) {
	err := &ConnectionRequiredError{}
	want := "database: connection configuration is required for remote mode"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestConnectionRequiredError_TypeAssertion(t *testing.T) {
	var err error = &ConnectionRequiredError{}
	var target *ConnectionRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *ConnectionRequiredError")
	}
}

func TestInvalidRaftClusterModeError_Message(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{"empty", "", `database: invalid raft cluster mode: ""`},
		{"unknown", "multi-paxos", `database: invalid raft cluster mode: "multi-paxos"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidRaftClusterModeError{Mode: tt.mode}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInvalidRaftClusterModeError_TypeAssertion(t *testing.T) {
	var err error = &InvalidRaftClusterModeError{Mode: "multi-paxos"}
	var target *InvalidRaftClusterModeError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *InvalidRaftClusterModeError")
	}
	if target.Mode != "multi-paxos" {
		t.Errorf("Mode = %q, want %q", target.Mode, "multi-paxos")
	}
}

func TestInvalidDiscoveryMethodError_Message(t *testing.T) {
	tests := []struct {
		name   string
		method string
		want   string
	}{
		{"empty", "", `database: invalid discovery method: ""`},
		{"unknown", "consul", `database: invalid discovery method: "consul"`},
		{"uppercase", "STATIC", `database: invalid discovery method: "STATIC"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidDiscoveryMethodError{Method: tt.method}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInvalidDiscoveryMethodError_TypeAssertion(t *testing.T) {
	var err error = &InvalidDiscoveryMethodError{Method: "consul"}
	var target *InvalidDiscoveryMethodError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *InvalidDiscoveryMethodError")
	}
	if target.Method != "consul" {
		t.Errorf("Method = %q, want %q", target.Method, "consul")
	}
}

func TestDiscoveryConfigRequiredError_Message(t *testing.T) {
	tests := []struct {
		name   string
		method string
		want   string
	}{
		{"multicast", "multicast", `database: discovery configuration required for method: "multicast"`},
		{"dns", "dns", `database: discovery configuration required for method: "dns"`},
		{"token", "token", `database: discovery configuration required for method: "token"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &DiscoveryConfigRequiredError{Method: tt.method}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscoveryConfigRequiredError_TypeAssertion(t *testing.T) {
	var err error = &DiscoveryConfigRequiredError{Method: "multicast"}
	var target *DiscoveryConfigRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DiscoveryConfigRequiredError")
	}
	if target.Method != "multicast" {
		t.Errorf("Method = %q, want %q", target.Method, "multicast")
	}
}

func TestDiscoverySharedSecretRequiredError_Message(t *testing.T) {
	err := &DiscoverySharedSecretRequiredError{Method: "multicast"}
	want := `database: shared secret required for discovery method: "multicast"`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDiscoverySharedSecretRequiredError_TypeAssertion(t *testing.T) {
	var err error = &DiscoverySharedSecretRequiredError{Method: "multicast"}
	var target *DiscoverySharedSecretRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DiscoverySharedSecretRequiredError")
	}
	if target.Method != "multicast" {
		t.Errorf("Method = %q, want %q", target.Method, "multicast")
	}
}

func TestDiscoveryDomainRequiredError_Message(t *testing.T) {
	err := &DiscoveryDomainRequiredError{}
	want := "database: domain is required for dns discovery"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDiscoveryDomainRequiredError_TypeAssertion(t *testing.T) {
	var err error = &DiscoveryDomainRequiredError{}
	var target *DiscoveryDomainRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DiscoveryDomainRequiredError")
	}
}

func TestDiscoveryEndpointRequiredError_Message(t *testing.T) {
	err := &DiscoveryEndpointRequiredError{}
	want := "database: endpoint is required for token discovery"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDiscoveryEndpointRequiredError_TypeAssertion(t *testing.T) {
	var err error = &DiscoveryEndpointRequiredError{}
	var target *DiscoveryEndpointRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DiscoveryEndpointRequiredError")
	}
}

func TestDiscoveryTokenRequiredError_Message(t *testing.T) {
	err := &DiscoveryTokenRequiredError{}
	want := "database: token is required for token discovery"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDiscoveryTokenRequiredError_TypeAssertion(t *testing.T) {
	var err error = &DiscoveryTokenRequiredError{}
	var target *DiscoveryTokenRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DiscoveryTokenRequiredError")
	}
}

func TestDiscoveryClusterSizeError_Message(t *testing.T) {
	tests := []struct {
		name string
		size int
		want string
	}{
		{"zero", 0, "database: invalid cluster size for token discovery: 0"},
		{"negative", -5, "database: invalid cluster size for token discovery: -5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &DiscoveryClusterSizeError{Size: tt.size}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscoveryClusterSizeError_TypeAssertion(t *testing.T) {
	var err error = &DiscoveryClusterSizeError{Size: 0}
	var target *DiscoveryClusterSizeError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *DiscoveryClusterSizeError")
	}
	if target.Size != 0 {
		t.Errorf("Size = %d, want %d", target.Size, 0)
	}
}

func TestAllErrorTypes_ImplementErrorInterface(t *testing.T) {
	cause := errors.New("test cause")
	errs := []error{
		&InvalidNameError{Name: "x"},
		&InvalidBackendError{Backend: "x"},
		&AlreadyExistsError{Name: "x"},
		&NotFoundError{Name: "x"},
		&NotOpenError{Name: "x"},
		&OpenError{Name: "x", Err: cause},
		&CloseError{Name: "x", Err: cause},
		&MetaReadError{Path: "x", Err: cause},
		&MetaWriteError{Path: "x", Err: cause},
		&LegacyMetadataError{Path: "x", Err: cause},
		&DropError{Name: "x", Err: cause},
		&StorageCreateError{Name: "x", Backend: "y", Err: cause},
		&InvalidModeError{Mode: "x"},
		&ConnectionRequiredError{},
		&InvalidRaftClusterModeError{Mode: "x"},
		&InvalidDiscoveryMethodError{Method: "x"},
		&DiscoveryConfigRequiredError{Method: "x"},
		&DiscoverySharedSecretRequiredError{Method: "x"},
		&DiscoveryDomainRequiredError{},
		&DiscoveryEndpointRequiredError{},
		&DiscoveryTokenRequiredError{},
		&DiscoveryClusterSizeError{Size: 0},
	}
	for _, err := range errs {
		if err.Error() == "" {
			t.Errorf("%T.Error() returned empty string", err)
		}
	}
}

func TestWrappingErrors_UnwrapReturnsCorrectCause(t *testing.T) {
	sentinel := errors.New("root cause")
	tests := []struct {
		name string
		err  error
	}{
		{"OpenError", &OpenError{Name: "db", Err: sentinel}},
		{"CloseError", &CloseError{Name: "db", Err: sentinel}},
		{"MetaReadError", &MetaReadError{Path: "/p", Err: sentinel}},
		{"MetaWriteError", &MetaWriteError{Path: "/p", Err: sentinel}},
		{"DropError", &DropError{Name: "db", Err: sentinel}},
		{"StorageCreateError", &StorageCreateError{Name: "db", Backend: "pebble", Err: sentinel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, sentinel) {
				t.Errorf("errors.Is(%T, sentinel) = false, want true", tt.err)
			}
		})
	}
}

func TestTenantIDRequiredError_Message(t *testing.T) {
	err := &TenantIDRequiredError{Operation: "CreateForTenant"}
	want := "database: tenant ID required for operation: CreateForTenant"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestTenantIDRequiredError_TypeAssertion(t *testing.T) {
	var err error = &TenantIDRequiredError{Operation: "GetForTenant"}
	var target *TenantIDRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *TenantIDRequiredError")
	}
	if target.Operation != "GetForTenant" {
		t.Errorf("Operation = %q, want %q", target.Operation, "GetForTenant")
	}
}

func TestSealedError_Message(t *testing.T) {
	err := &SealedError{}
	want := "database: operation rejected \u2014 database is sealed"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestSealedError_TypeAssertion(t *testing.T) {
	var err error = &SealedError{}
	var target *SealedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *SealedError")
	}
}

func TestBarrierDecryptError_Message(t *testing.T) {
	cause := errors.New("decryption failed")
	err := &BarrierDecryptError{Key: "mykey", Err: cause}
	want := `database: barrier decrypt failed for key "mykey": decryption failed`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestBarrierDecryptError_Unwrap(t *testing.T) {
	cause := errors.New("aes failure")
	err := &BarrierDecryptError{Key: "k", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *BarrierDecryptError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *BarrierDecryptError")
	}
	if target.Key != "k" {
		t.Errorf("Key = %q, want %q", target.Key, "k")
	}
}

func TestBarrierEncryptError_Message(t *testing.T) {
	cause := errors.New("encryption failed")
	err := &BarrierEncryptError{Key: "secretkey", Err: cause}
	want := `database: barrier encrypt failed for key "secretkey": encryption failed`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestBarrierEncryptError_Unwrap(t *testing.T) {
	cause := errors.New("key unavailable")
	err := &BarrierEncryptError{Key: "data-key", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *BarrierEncryptError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *BarrierEncryptError")
	}
	if target.Key != "data-key" {
		t.Errorf("Key = %q, want %q", target.Key, "data-key")
	}
}

func TestEtcdListenAddrRequiredError_Message(t *testing.T) {
	err := &EtcdListenAddrRequiredError{}
	want := "database: etcd listen_addr is required when etcd is enabled"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestEtcdListenAddrRequiredError_TypeAssertion(t *testing.T) {
	var err error = &EtcdListenAddrRequiredError{}
	var target *EtcdListenAddrRequiredError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *EtcdListenAddrRequiredError")
	}
}

// The message tests below cover the typed errors whose Error() had no test at
// all. A typed error's message IS its public surface -- it is what an operator
// reads and what a support transcript quotes -- and an untested one drifts the
// moment a field is renamed, because nothing else in the package reads it.
//
// Each case asserts the WHOLE string rather than a substring: a partial match
// passes when a field silently stops being interpolated, which is the failure
// these are for.

func TestDropInProgressError_Message(t *testing.T) {
	err := &DropInProgressError{Name: "orders"}
	want := `database: drop already in progress for database "orders"`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDroppingError_Message(t *testing.T) {
	err := &DroppingError{Name: "orders"}
	want := `database: database is being dropped: "orders"`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestKeyspaceReclaimError_Message(t *testing.T) {
	err := &KeyspaceReclaimError{Name: "orders", TenantID: "acme", Err: io.ErrUnexpectedEOF}
	want := `database: failed to reclaim the shared-store keyspace of database "orders" (tenant: "acme"): unexpected EOF`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestUnsupportedBarrierStrategyError_Message(t *testing.T) {
	err := &UnsupportedBarrierStrategyError{Mode: "kmip"}
	want := `database: unsupported barrier strategy: "kmip"`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestInvalidDataDirError_Message(t *testing.T) {
	err := &InvalidDataDirError{Name: "orders", DataDir: "/var/lib/qrdb/orders", Err: io.ErrUnexpectedEOF}
	want := `database: invalid data directory for database "orders" ("/var/lib/qrdb/orders"): unexpected EOF`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestInvalidDataDirError_Unwrap: the cause must survive errors.Is, or a
// caller cannot distinguish a permission problem from a missing path without
// string matching.
func TestInvalidDataDirError_Unwrap(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	err := &InvalidDataDirError{Name: "orders", DataDir: "/var/lib/qrdb/orders", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is must reach the cause through Unwrap")
	}
}

func TestDataDirReservedPathError_Message(t *testing.T) {
	err := &DataDirReservedPathError{Name: "orders", DataDir: "/var/lib/qrdb/raft/orders", Reserved: "/var/lib/qrdb/raft"}
	want := `database: data directory for database "orders" overlaps a reserved path "/var/lib/qrdb/raft": "/var/lib/qrdb/raft/orders"`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDataDirConflictError_Message(t *testing.T) {
	err := &DataDirConflictError{Name: "orders", DataDir: "/var/lib/qrdb/shared", With: "invoices"}
	want := `database: data directory for database "orders" conflicts with database "invoices": "/var/lib/qrdb/shared"`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestBarrierMigrationNotSupportedError_Message covers both directions: the
// Direction field is interpolated verbatim into the sentence, so a value that
// does not read as a verb produces a malformed message, and the test is where
// that shows up.
func TestBarrierMigrationNotSupportedError_Message(t *testing.T) {
	for _, tc := range []struct{ direction, want string }{
		{"enable", `database: cannot enable the encryption barrier for database "orders" after creation: no re-encryption migration is available; create the database with the desired barrier configuration instead`},
		{"disable", `database: cannot disable the encryption barrier for database "orders" after creation: no re-encryption migration is available; create the database with the desired barrier configuration instead`},
	} {
		t.Run(tc.direction, func(t *testing.T) {
			err := &BarrierMigrationNotSupportedError{Name: "orders", Direction: tc.direction}
			if got := err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBarrierEncryptorUnavailableError_Message(t *testing.T) {
	err := &BarrierEncryptorUnavailableError{Name: "orders"}
	want := `database: barrier encryptor unavailable for database "orders" immediately after registration`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
