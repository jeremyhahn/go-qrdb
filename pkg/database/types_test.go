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
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestBackendType_Valid_RecognizedTypes(t *testing.T) {
	tests := []struct {
		name    string
		backend BackendType
	}{
		{"memory", BackendMemory},
		{"file", BackendFile},
		{"pebble", BackendPebble},
		{"raft", BackendRaft},
		{"objstore", BackendObjstore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.backend.Valid() {
				t.Errorf("BackendType(%q).Valid() = false, want true", tt.backend)
			}
		})
	}
}

func TestBackends_ListsExactlyTheValidSet(t *testing.T) {
	got := Backends()
	if len(got) != len(validBackends) {
		t.Fatalf("Backends() = %v, want the %d valid backends", got, len(validBackends))
	}
	for _, b := range got {
		if !b.Valid() {
			t.Errorf("Backends() lists %q, which Valid rejects", b)
		}
	}
	if !slices.Contains(got, BackendObjstore) {
		t.Errorf("Backends() = %v, missing %q", got, BackendObjstore)
	}
}

func TestBackends_SortedAndFresh(t *testing.T) {
	got := Backends()
	if !slices.IsSorted(got) {
		t.Errorf("Backends() = %v, want sorted", got)
	}
	got[0] = "mutated"
	if Backends()[0] == "mutated" {
		t.Error("Backends() returned a shared slice; a caller's write leaked into the next call")
	}
}

func TestBackendType_Valid_UnrecognizedTypes(t *testing.T) {
	tests := []struct {
		name    string
		backend BackendType
	}{
		{"empty", BackendType("")},
		{"unknown", BackendType("unknown")},
		{"uppercase_MEMORY", BackendType("MEMORY")},
		{"sqlite", BackendType("sqlite")},
		{"spaces", BackendType("memory ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.backend.Valid() {
				t.Errorf("BackendType(%q).Valid() = true, want false", tt.backend)
			}
		})
	}
}

func TestBackendType_Constants(t *testing.T) {
	if BackendMemory != "memory" {
		t.Errorf("BackendMemory = %q, want %q", BackendMemory, "memory")
	}
	if BackendFile != "file" {
		t.Errorf("BackendFile = %q, want %q", BackendFile, "file")
	}
	if BackendPebble != "pebble" {
		t.Errorf("BackendPebble = %q, want %q", BackendPebble, "pebble")
	}
	if BackendRaft != "raft" {
		t.Errorf("BackendRaft = %q, want %q", BackendRaft, "raft")
	}
}

func TestStatusType_Constants(t *testing.T) {
	if StatusOpen != "open" {
		t.Errorf("StatusOpen = %q, want %q", StatusOpen, "open")
	}
	if StatusClosed != "closed" {
		t.Errorf("StatusClosed = %q, want %q", StatusClosed, "closed")
	}
	if StatusError != "error" {
		t.Errorf("StatusError = %q, want %q", StatusError, "error")
	}
}

func TestDatabaseConfig_Validate_ValidConfigs(t *testing.T) {
	tests := []struct {
		name   string
		config DatabaseConfig
	}{
		{
			name:   "kv_memory",
			config: DatabaseConfig{Name: "testdb", Backend: BackendMemory},
		},
		{
			name:   "timeseries_pebble",
			config: DatabaseConfig{Name: "metrics-db", Backend: BackendPebble},
		},
		{
			name:   "vector_raft",
			config: DatabaseConfig{Name: "embeddings_store", Backend: BackendRaft},
		},
		{
			name:   "kv_file",
			config: DatabaseConfig{Name: "my-db-123", Backend: BackendFile},
		},
		{
			name:   "alphanumeric_only",
			config: DatabaseConfig{Name: "abc123", Backend: BackendMemory},
		},
		{
			name:   "hyphens_and_underscores",
			config: DatabaseConfig{Name: "my-test_db-01", Backend: BackendMemory},
		},
		{
			name:   "single_char_name",
			config: DatabaseConfig{Name: "a", Backend: BackendMemory},
		},
		{
			name:   "with_data_dir",
			config: DatabaseConfig{Name: "testdb", Backend: BackendMemory, DataDir: "/tmp/data"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.config.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestDatabaseConfig_Validate_EmptyName(t *testing.T) {
	cfg := DatabaseConfig{Name: "", Backend: BackendMemory}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want *InvalidNameError")
	}
	var nameErr *InvalidNameError
	if !errors.As(err, &nameErr) {
		t.Fatalf("Validate() error type = %T, want *InvalidNameError", err)
	}
	if nameErr.Name != "" {
		t.Errorf("InvalidNameError.Name = %q, want %q", nameErr.Name, "")
	}
}

func TestDatabaseConfig_Validate_InvalidNameCharacters(t *testing.T) {
	tests := []struct {
		name     string
		dbName   string
		wantName string
	}{
		{"dot", "my.db", "my.db"},
		{"slash", "my/db", "my/db"},
		{"space", "my db", "my db"},
		{"backslash", "my\\db", "my\\db"},
		{"at_sign", "my@db", "my@db"},
		{"colon", "my:db", "my:db"},
		{"asterisk", "my*db", "my*db"},
		{"leading_space", " mydb", " mydb"},
		{"trailing_space", "mydb ", "mydb "},
		{"exclamation", "my!db", "my!db"},
		{"question_mark", "my?db", "my?db"},
		{"equals_sign", "my=db", "my=db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseConfig{Name: tt.dbName, Backend: BackendMemory}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *InvalidNameError")
			}
			var nameErr *InvalidNameError
			if !errors.As(err, &nameErr) {
				t.Fatalf("Validate() error type = %T, want *InvalidNameError", err)
			}
			if nameErr.Name != tt.wantName {
				t.Errorf("InvalidNameError.Name = %q, want %q", nameErr.Name, tt.wantName)
			}
		})
	}
}

func TestDatabaseConfig_Validate_InvalidBackend(t *testing.T) {
	tests := []struct {
		name        string
		backend     BackendType
		wantBackend string
	}{
		{"empty_backend", BackendType(""), ""},
		{"unknown_backend", BackendType("sqlite"), "sqlite"},
		{"uppercase_backend", BackendType("MEMORY"), "MEMORY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseConfig{Name: "testdb", Backend: tt.backend}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *InvalidBackendError")
			}
			var backendErr *InvalidBackendError
			if !errors.As(err, &backendErr) {
				t.Fatalf("Validate() error type = %T, want *InvalidBackendError", err)
			}
			if backendErr.Backend != tt.wantBackend {
				t.Errorf("InvalidBackendError.Backend = %q, want %q", backendErr.Backend, tt.wantBackend)
			}
		})
	}
}

func TestDatabaseConfig_Validate_ErrorPriority(t *testing.T) {
	// Name validation runs first, so an invalid name should take priority
	// over an invalid backend.
	cfg := DatabaseConfig{Name: "", Backend: BackendType("bad")}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want error")
	}
	var nameErr *InvalidNameError
	if !errors.As(err, &nameErr) {
		t.Fatalf("expected *InvalidNameError first, got %T", err)
	}
}

func TestProtocolConfig_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		sql  ProtocolConfig
	}{
		{
			name: "both_enabled",
			sql:  ProtocolConfig{MySQL: true, PostgreSQL: true},
		},
		{
			name: "mysql_only",
			sql:  ProtocolConfig{MySQL: true, PostgreSQL: false},
		},
		{
			name: "postgresql_only",
			sql:  ProtocolConfig{MySQL: false, PostgreSQL: true},
		},
		{
			name: "both_disabled",
			sql:  ProtocolConfig{MySQL: false, PostgreSQL: false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.sql)
			if err != nil {
				t.Fatalf("Marshal() returned unexpected error: %v", err)
			}

			var got ProtocolConfig
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() returned unexpected error: %v", err)
			}

			if got.MySQL != tt.sql.MySQL {
				t.Errorf("MySQL = %v, want %v", got.MySQL, tt.sql.MySQL)
			}
			if got.PostgreSQL != tt.sql.PostgreSQL {
				t.Errorf("PostgreSQL = %v, want %v", got.PostgreSQL, tt.sql.PostgreSQL)
			}
		})
	}
}

func TestDatabaseMeta_BackwardCompat_MissingSQL(t *testing.T) {
	// Simulate JSON from before the Protocol field was added.
	raw := `{"name":"legacy-db","engine":"kv","backend":"memory"}`

	var meta DatabaseMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("Unmarshal() returned unexpected error: %v", err)
	}

	if meta.Name != "legacy-db" {
		t.Errorf("Name = %q, want %q", meta.Name, "legacy-db")
	}
	if meta.Backend != BackendMemory {
		t.Errorf("Backend = %q, want %q", meta.Backend, BackendMemory)
	}
	// Protocol should default to zero-value (both false).
	if meta.Protocol.MySQL {
		t.Error("Protocol.MySQL = true, want false for missing JSON field")
	}
	if meta.Protocol.PostgreSQL {
		t.Error("Protocol.PostgreSQL = true, want false for missing JSON field")
	}
}

func TestDatabaseInfo_ProtocolConfig_OmitEmpty(t *testing.T) {
	// Go's encoding/json omitempty for struct types includes the field
	// even when all inner fields are zero because the struct itself is
	// not the zero value (only nil pointers, empty slices/maps, zero
	// numbers, empty strings, and false booleans count). Verify that the
	// sql field is always present with the correct values.
	info := DatabaseInfo{
		Name:     "omit-db",
		Backend:  BackendMemory,
		Status:   StatusOpen,
		Protocol: ProtocolConfig{MySQL: false, PostgreSQL: false},
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal() returned unexpected error: %v", err)
	}

	// Verify the zero-value Protocol config serializes with both booleans false.
	content := string(data)
	if !containsSubstring(content, `"mysql":false`) {
		t.Errorf("JSON missing mysql:false for zero-value ProtocolConfig; got: %s", content)
	}
	if !containsSubstring(content, `"postgresql":false`) {
		t.Errorf("JSON missing postgresql:false for zero-value ProtocolConfig; got: %s", content)
	}

	// Verify that when Protocol has non-zero values, they serialize correctly.
	info.Protocol = ProtocolConfig{MySQL: true, PostgreSQL: false}
	data, err = json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal() returned unexpected error: %v", err)
	}
	content = string(data)
	if !containsSubstring(content, `"mysql":true`) {
		t.Errorf("JSON missing mysql:true; got: %s", content)
	}
	if !containsSubstring(content, `"postgresql":false`) {
		t.Errorf("JSON missing postgresql:false; got: %s", content)
	}

	// DatabaseMeta uses omitempty on the Protocol field. Verify it is omitted
	// when Protocol is the zero value.
	meta := DatabaseMeta{
		Name:     "omit-meta-db",
		Backend:  BackendMemory,
		Protocol: ProtocolConfig{},
	}
	data, err = json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal(meta) returned unexpected error: %v", err)
	}
	_ = string(data)

	// DatabaseMeta also uses omitempty on a struct; Go includes it anyway.
	// Verify that when Protocol has values, they appear correctly.
	meta.Protocol = ProtocolConfig{MySQL: true, PostgreSQL: true}
	data, err = json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal(meta with Protocol) returned unexpected error: %v", err)
	}
	content = string(data)
	if !containsSubstring(content, `"mysql":true`) {
		t.Errorf("meta JSON missing mysql:true; got: %s", content)
	}
	if !containsSubstring(content, `"postgresql":true`) {
		t.Errorf("meta JSON missing postgresql:true; got: %s", content)
	}
}

func TestDatabaseMode_Valid_RecognizedTypes(t *testing.T) {
	tests := []struct {
		name string
		mode DatabaseMode
	}{
		{"embedded", ModeEmbedded},
		{"remote", ModeRemote},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.mode.Valid() {
				t.Errorf("DatabaseMode(%q).Valid() = false, want true", tt.mode)
			}
		})
	}
}

func TestDatabaseMode_Valid_UnrecognizedTypes(t *testing.T) {
	tests := []struct {
		name string
		mode DatabaseMode
	}{
		{"empty", DatabaseMode("")},
		{"unknown", DatabaseMode("unknown")},
		{"uppercase_EMBEDDED", DatabaseMode("EMBEDDED")},
		{"hybrid", DatabaseMode("hybrid")},
		{"spaces", DatabaseMode("embedded ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mode.Valid() {
				t.Errorf("DatabaseMode(%q).Valid() = true, want false", tt.mode)
			}
		})
	}
}

func TestDatabaseMode_Constants(t *testing.T) {
	if ModeEmbedded != "embedded" {
		t.Errorf("ModeEmbedded = %q, want %q", ModeEmbedded, "embedded")
	}
	if ModeRemote != "remote" {
		t.Errorf("ModeRemote = %q, want %q", ModeRemote, "remote")
	}
}

func TestRaftClusterMode_Valid_RecognizedTypes(t *testing.T) {
	tests := []struct {
		name string
		mode RaftClusterMode
	}{
		{"none", RaftNone},
		{"local", RaftLocal},
		{"distributed", RaftDistributed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.mode.Valid() {
				t.Errorf("RaftClusterMode(%q).Valid() = false, want true", tt.mode)
			}
		})
	}
}

func TestRaftClusterMode_Valid_UnrecognizedTypes(t *testing.T) {
	tests := []struct {
		name string
		mode RaftClusterMode
	}{
		{"empty", RaftClusterMode("")},
		{"unknown", RaftClusterMode("unknown")},
		{"uppercase_NONE", RaftClusterMode("NONE")},
		{"clustered", RaftClusterMode("clustered")},
		{"spaces", RaftClusterMode("none ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mode.Valid() {
				t.Errorf("RaftClusterMode(%q).Valid() = true, want false", tt.mode)
			}
		})
	}
}

func TestRaftClusterMode_Constants(t *testing.T) {
	if RaftNone != "none" {
		t.Errorf("RaftNone = %q, want %q", RaftNone, "none")
	}
	if RaftLocal != "local" {
		t.Errorf("RaftLocal = %q, want %q", RaftLocal, "local")
	}
	if RaftDistributed != "distributed" {
		t.Errorf("RaftDistributed = %q, want %q", RaftDistributed, "distributed")
	}
}

func TestDiscoveryMethod_Valid_RecognizedTypes(t *testing.T) {
	tests := []struct {
		name   string
		method DiscoveryMethod
	}{
		{"static", DiscoveryStatic},
		{"multicast", DiscoveryMulticast},
		{"dns", DiscoveryDNS},
		{"token", DiscoveryToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.method.Valid() {
				t.Errorf("DiscoveryMethod(%q).Valid() = false, want true", tt.method)
			}
		})
	}
}

func TestDiscoveryMethod_Valid_UnrecognizedTypes(t *testing.T) {
	tests := []struct {
		name   string
		method DiscoveryMethod
	}{
		{"empty", DiscoveryMethod("")},
		{"unknown", DiscoveryMethod("unknown")},
		{"uppercase_STATIC", DiscoveryMethod("STATIC")},
		{"spaces", DiscoveryMethod("static ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.method.Valid() {
				t.Errorf("DiscoveryMethod(%q).Valid() = true, want false", tt.method)
			}
		})
	}
}

func TestDiscoveryMethod_Constants(t *testing.T) {
	if DiscoveryStatic != "static" {
		t.Errorf("DiscoveryStatic = %q, want %q", DiscoveryStatic, "static")
	}
	if DiscoveryMulticast != "multicast" {
		t.Errorf("DiscoveryMulticast = %q, want %q", DiscoveryMulticast, "multicast")
	}
	if DiscoveryDNS != "dns" {
		t.Errorf("DiscoveryDNS = %q, want %q", DiscoveryDNS, "dns")
	}
	if DiscoveryToken != "token" {
		t.Errorf("DiscoveryToken = %q, want %q", DiscoveryToken, "token")
	}
}

func TestMulticastDiscoveryConfig_Validate_Valid(t *testing.T) {
	tests := []struct {
		name string
		cfg  MulticastDiscoveryConfig
	}{
		{
			name: "minimal",
			cfg:  MulticastDiscoveryConfig{SharedSecret: "my-secret"},
		},
		{
			name: "full",
			cfg: MulticastDiscoveryConfig{
				SharedSecret:     "cluster-secret-key",
				MulticastGroup:   "239.1.2.3:9876",
				Interface:        "eth0",
				MinPeers:         3,
				AnnounceInterval: "5s",
				DiscoverTimeout:  "30s",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestMulticastDiscoveryConfig_Validate_MissingSecret(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := MulticastDiscoveryConfig{SharedSecret: tt.secret}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *DiscoverySharedSecretRequiredError")
			}
			var secretErr *DiscoverySharedSecretRequiredError
			if !errors.As(err, &secretErr) {
				t.Fatalf("Validate() error type = %T, want *DiscoverySharedSecretRequiredError", err)
			}
			if secretErr.Method != "multicast" {
				t.Errorf("Method = %q, want %q", secretErr.Method, "multicast")
			}
		})
	}
}

func TestDNSDiscoveryConfig_Validate_Valid(t *testing.T) {
	tests := []struct {
		name string
		cfg  DNSDiscoveryConfig
	}{
		{
			name: "minimal",
			cfg:  DNSDiscoveryConfig{Domain: "cluster.example.com"},
		},
		{
			name: "full",
			cfg: DNSDiscoveryConfig{
				Service:       "_raft",
				Proto:         "_tcp",
				Domain:        "cluster.example.com",
				LookupTimeout: "10s",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestDNSDiscoveryConfig_Validate_MissingDomain(t *testing.T) {
	tests := []struct {
		name   string
		domain string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DNSDiscoveryConfig{Domain: tt.domain}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *DiscoveryDomainRequiredError")
			}
			var domainErr *DiscoveryDomainRequiredError
			if !errors.As(err, &domainErr) {
				t.Fatalf("Validate() error type = %T, want *DiscoveryDomainRequiredError", err)
			}
		})
	}
}

func TestTokenDiscoveryConfig_Validate_Valid(t *testing.T) {
	tests := []struct {
		name string
		cfg  TokenDiscoveryConfig
	}{
		{
			name: "minimal",
			cfg:  TokenDiscoveryConfig{Endpoint: "https://discovery.example.com", Token: "abc123", ClusterSize: 3},
		},
		{
			name: "with_timeout",
			cfg: TokenDiscoveryConfig{
				Endpoint:    "https://discovery.example.com/v1",
				Token:       "long-secure-token",
				ClusterSize: 5,
				Timeout:     "30s",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestTokenDiscoveryConfig_Validate_MissingEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := TokenDiscoveryConfig{Endpoint: tt.endpoint, Token: "abc", ClusterSize: 3}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *DiscoveryEndpointRequiredError")
			}
			var endpointErr *DiscoveryEndpointRequiredError
			if !errors.As(err, &endpointErr) {
				t.Fatalf("Validate() error type = %T, want *DiscoveryEndpointRequiredError", err)
			}
		})
	}
}

func TestTokenDiscoveryConfig_Validate_MissingToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := TokenDiscoveryConfig{Endpoint: "https://example.com", Token: tt.token, ClusterSize: 3}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *DiscoveryTokenRequiredError")
			}
			var tokenErr *DiscoveryTokenRequiredError
			if !errors.As(err, &tokenErr) {
				t.Fatalf("Validate() error type = %T, want *DiscoveryTokenRequiredError", err)
			}
		})
	}
}

func TestTokenDiscoveryConfig_Validate_InvalidClusterSize(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := TokenDiscoveryConfig{Endpoint: "https://example.com", Token: "abc", ClusterSize: tt.size}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *DiscoveryClusterSizeError")
			}
			var sizeErr *DiscoveryClusterSizeError
			if !errors.As(err, &sizeErr) {
				t.Fatalf("Validate() error type = %T, want *DiscoveryClusterSizeError", err)
			}
			if sizeErr.Size != tt.size {
				t.Errorf("Size = %d, want %d", sizeErr.Size, tt.size)
			}
		})
	}
}

func TestDatabaseRaftConfig_Validate_ValidConfigs(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseRaftConfig
	}{
		{
			name: "none_mode",
			cfg:  DatabaseRaftConfig{ClusterMode: RaftNone},
		},
		{
			name: "local_mode",
			cfg:  DatabaseRaftConfig{ClusterMode: RaftLocal, LocalNodeCount: 3},
		},
		{
			name: "distributed_static_implicit",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				ShardID:     1,
				ReplicaID:   1,
				RaftAddress: "localhost:63001",
				Peers:       []string{"localhost:63002", "localhost:63003"},
			},
		},
		{
			name: "distributed_static_explicit",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryStatic,
				Peers:       []string{"localhost:63002"},
			},
		},
		{
			name: "distributed_multicast",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryMulticast,
				Multicast:   &MulticastDiscoveryConfig{SharedSecret: "secret"},
			},
		},
		{
			name: "distributed_dns",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryDNS,
				DNS:         &DNSDiscoveryConfig{Domain: "cluster.example.com"},
			},
		},
		{
			name: "distributed_token",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryToken,
				Token:       &TokenDiscoveryConfig{Endpoint: "https://example.com", Token: "abc", ClusterSize: 3},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestDatabaseRaftConfig_Validate_InvalidClusterMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     RaftClusterMode
		wantMode string
	}{
		{"empty_mode", RaftClusterMode(""), ""},
		{"unknown_mode", RaftClusterMode("clustered"), "clustered"},
		{"uppercase_mode", RaftClusterMode("DISTRIBUTED"), "DISTRIBUTED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseRaftConfig{ClusterMode: tt.mode}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *InvalidRaftClusterModeError")
			}
			var modeErr *InvalidRaftClusterModeError
			if !errors.As(err, &modeErr) {
				t.Fatalf("Validate() error type = %T, want *InvalidRaftClusterModeError", err)
			}
			if modeErr.Mode != tt.wantMode {
				t.Errorf("InvalidRaftClusterModeError.Mode = %q, want %q", modeErr.Mode, tt.wantMode)
			}
		})
	}
}

func TestDatabaseRaftConfig_Validate_InvalidDiscoveryMethod(t *testing.T) {
	tests := []struct {
		name       string
		method     DiscoveryMethod
		wantMethod string
	}{
		{"unknown_method", DiscoveryMethod("consul"), "consul"},
		{"uppercase_method", DiscoveryMethod("STATIC"), "STATIC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseRaftConfig{ClusterMode: RaftDistributed, Discovery: tt.method}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *InvalidDiscoveryMethodError")
			}
			var methodErr *InvalidDiscoveryMethodError
			if !errors.As(err, &methodErr) {
				t.Fatalf("Validate() error type = %T, want *InvalidDiscoveryMethodError", err)
			}
			if methodErr.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", methodErr.Method, tt.wantMethod)
			}
		})
	}
}

func TestDatabaseRaftConfig_Validate_MissingDiscoveryConfig(t *testing.T) {
	tests := []struct {
		name       string
		method     DiscoveryMethod
		wantMethod string
	}{
		{"multicast_nil", DiscoveryMulticast, "multicast"},
		{"dns_nil", DiscoveryDNS, "dns"},
		{"token_nil", DiscoveryToken, "token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseRaftConfig{ClusterMode: RaftDistributed, Discovery: tt.method}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *DiscoveryConfigRequiredError")
			}
			var configErr *DiscoveryConfigRequiredError
			if !errors.As(err, &configErr) {
				t.Fatalf("Validate() error type = %T, want *DiscoveryConfigRequiredError", err)
			}
			if configErr.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", configErr.Method, tt.wantMethod)
			}
		})
	}
}

func TestDatabaseRaftConfig_Validate_DefaultsToStatic(t *testing.T) {
	cfg := DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Peers:       []string{"localhost:63001"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if cfg.Discovery != DiscoveryStatic {
		t.Errorf("Discovery = %q, want %q after defaulting", cfg.Discovery, DiscoveryStatic)
	}
}

func TestDatabaseRaftConfig_Validate_DiscoverySkippedForNonDistributed(t *testing.T) {
	tests := []struct {
		name string
		mode RaftClusterMode
	}{
		{"none_mode", RaftNone},
		{"local_mode", RaftLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set an invalid discovery method; it should be ignored for non-distributed modes.
			cfg := DatabaseRaftConfig{
				ClusterMode: tt.mode,
				Discovery:   DiscoveryMethod("invalid"),
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestDatabaseConnectionConfig_Validate_Valid(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseConnectionConfig
	}{
		{
			name: "grpc_transport",
			cfg:  DatabaseConnectionConfig{Transport: "grpc", Address: "localhost:9090"},
		},
		{
			name: "rest_transport",
			cfg:  DatabaseConnectionConfig{Transport: "rest", Address: "https://db.example.com"},
		},
		{
			name: "unix_transport",
			cfg:  DatabaseConnectionConfig{Transport: "unix", Address: "/var/run/qrdb.sock"},
		},
		{
			name: "with_tls_files",
			cfg: DatabaseConnectionConfig{
				Transport: "grpc",
				Address:   "localhost:9090",
				CAFile:    "/etc/certs/ca.pem",
				CertFile:  "/etc/certs/client.pem",
				KeyFile:   "/etc/certs/client-key.pem",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestDatabaseConnectionConfig_Validate_MissingTransport(t *testing.T) {
	tests := []struct {
		name      string
		transport string
	}{
		{"empty_transport", ""},
		{"whitespace_transport", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseConnectionConfig{Transport: tt.transport, Address: "localhost:9090"}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *ConnectionRequiredError")
			}
			var connErr *ConnectionRequiredError
			if !errors.As(err, &connErr) {
				t.Fatalf("Validate() error type = %T, want *ConnectionRequiredError", err)
			}
		})
	}
}

func TestDatabaseConnectionConfig_Validate_MissingAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{"empty_address", ""},
		{"whitespace_address", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseConnectionConfig{Transport: "grpc", Address: tt.address}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() returned nil, want *ConnectionRequiredError")
			}
			var connErr *ConnectionRequiredError
			if !errors.As(err, &connErr) {
				t.Fatalf("Validate() error type = %T, want *ConnectionRequiredError", err)
			}
		})
	}
}

func TestDatabaseConfig_Validate_EmptyModeDefaultsToEmbedded(t *testing.T) {
	cfg := DatabaseConfig{
		Name:    "testdb",
		Backend: BackendMemory,
		Mode:    "", // empty mode
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if cfg.Mode != ModeEmbedded {
		t.Errorf("Mode = %q, want %q after defaulting", cfg.Mode, ModeEmbedded)
	}
}

func TestDatabaseConfig_Validate_RemoteRequiresConnection(t *testing.T) {
	cfg := DatabaseConfig{
		Name: "remote-db",
		Mode: ModeRemote,
		// Connection is nil
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want *ConnectionRequiredError")
	}
	var connErr *ConnectionRequiredError
	if !errors.As(err, &connErr) {
		t.Fatalf("Validate() error type = %T, want *ConnectionRequiredError", err)
	}
}

func TestDatabaseConfig_Validate_RemoteWithConnection(t *testing.T) {
	cfg := DatabaseConfig{
		Name: "remote-db",
		Mode: ModeRemote,
		Connection: &DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "localhost:9090",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

func TestDatabaseConfig_Validate_InvalidMode(t *testing.T) {
	cfg := DatabaseConfig{
		Name:    "testdb",
		Backend: BackendMemory,
		Mode:    DatabaseMode("hybrid"),
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want *InvalidModeError")
	}
	var modeErr *InvalidModeError
	if !errors.As(err, &modeErr) {
		t.Fatalf("Validate() error type = %T, want *InvalidModeError", err)
	}
	if modeErr.Mode != "hybrid" {
		t.Errorf("InvalidModeError.Mode = %q, want %q", modeErr.Mode, "hybrid")
	}
}

func TestDatabaseConfig_Validate_EmbeddedRequiresBackend(t *testing.T) {
	cfg := DatabaseConfig{
		Name:    "testdb",
		Backend: BackendType("invalid"),
		Mode:    ModeEmbedded,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want *InvalidBackendError")
	}
	var backendErr *InvalidBackendError
	if !errors.As(err, &backendErr) {
		t.Fatalf("Validate() error type = %T, want *InvalidBackendError", err)
	}
	if backendErr.Backend != "invalid" {
		t.Errorf("InvalidBackendError.Backend = %q, want %q", backendErr.Backend, "invalid")
	}
}

func TestDatabaseConfig_Validate_WithRaftConfig(t *testing.T) {
	// Valid embedded config with valid raft config.
	cfg := DatabaseConfig{
		Name:    "raft-db",
		Backend: BackendRaft,
		Mode:    ModeEmbedded,
		Raft:    &DatabaseRaftConfig{ClusterMode: RaftDistributed},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}

	// Valid embedded config with invalid raft config.
	cfg.Raft = &DatabaseRaftConfig{ClusterMode: RaftClusterMode("bad")}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want *InvalidRaftClusterModeError")
	}
	var raftErr *InvalidRaftClusterModeError
	if !errors.As(err, &raftErr) {
		t.Fatalf("Validate() error type = %T, want *InvalidRaftClusterModeError", err)
	}
}

func TestDatabaseConfig_Validate_RemoteSkipsBackendValidation(t *testing.T) {
	// Remote mode with an invalid backend should still pass because
	// backend validation is skipped for remote databases.
	cfg := DatabaseConfig{
		Name:    "remote-db",
		Backend: BackendType("invalid"),
		Mode:    ModeRemote,
		Connection: &DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "localhost:9090",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

func TestDatabaseMeta_BackwardCompat_MissingMode(t *testing.T) {
	// Simulate JSON from before the Mode field was added.
	raw := `{"name":"legacy-db","engine":"kv","backend":"memory"}`

	var meta DatabaseMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("Unmarshal() returned unexpected error: %v", err)
	}

	if meta.Name != "legacy-db" {
		t.Errorf("Name = %q, want %q", meta.Name, "legacy-db")
	}
	if meta.Backend != BackendMemory {
		t.Errorf("Backend = %q, want %q", meta.Backend, BackendMemory)
	}
	// Mode should default to zero-value (empty string).
	if meta.Mode != "" {
		t.Errorf("Mode = %q, want empty string for missing JSON field", meta.Mode)
	}
	// Raft, Barrier, Connection should be nil.
	if meta.Raft != nil {
		t.Error("Raft = non-nil, want nil for missing JSON field")
	}
	if meta.Barrier != nil {
		t.Error("Barrier = non-nil, want nil for missing JSON field")
	}
	if meta.Connection != nil {
		t.Error("Connection = non-nil, want nil for missing JSON field")
	}
}

func TestDatabaseInfo_NewFields_JSONRoundTrip(t *testing.T) {
	info := DatabaseInfo{
		Name:    "full-db",
		Backend: BackendRaft,
		Status:  StatusOpen,
		Mode:    ModeEmbedded,
		Raft: &DatabaseRaftConfig{
			ClusterMode: RaftDistributed,
			ShardID:     42,
			ReplicaID:   1,
			RaftAddress: "localhost:63001",
			Peers:       []string{"localhost:63002"},
			Discovery:   DiscoveryMulticast,
			Multicast:   &MulticastDiscoveryConfig{SharedSecret: "s3cret", MulticastGroup: "239.1.2.3:9876"},
		},
		Barrier: &DatabaseBarrierConfig{
			Enabled:        true,
			Mode:           "shamir",
			SharesRequired: 3,
			SharesTotal:    5,
		},
		Connection: &DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "localhost:9090",
			CAFile:    "/etc/ca.pem",
		},
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal() returned unexpected error: %v", err)
	}

	var got DatabaseInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() returned unexpected error: %v", err)
	}

	if got.Mode != info.Mode {
		t.Errorf("Mode = %q, want %q", got.Mode, info.Mode)
	}
	if got.Raft == nil {
		t.Fatal("Raft = nil, want non-nil")
	}
	if got.Raft.ClusterMode != RaftDistributed {
		t.Errorf("Raft.ClusterMode = %q, want %q", got.Raft.ClusterMode, RaftDistributed)
	}
	if got.Raft.ShardID != 42 {
		t.Errorf("Raft.ShardID = %d, want %d", got.Raft.ShardID, 42)
	}
	if got.Raft.ReplicaID != 1 {
		t.Errorf("Raft.ReplicaID = %d, want %d", got.Raft.ReplicaID, 1)
	}
	if got.Raft.RaftAddress != "localhost:63001" {
		t.Errorf("Raft.RaftAddress = %q, want %q", got.Raft.RaftAddress, "localhost:63001")
	}
	if len(got.Raft.Peers) != 1 || got.Raft.Peers[0] != "localhost:63002" {
		t.Errorf("Raft.Peers = %v, want [localhost:63002]", got.Raft.Peers)
	}
	if got.Raft.Discovery != DiscoveryMulticast {
		t.Errorf("Raft.Discovery = %q, want %q", got.Raft.Discovery, DiscoveryMulticast)
	}
	if got.Raft.Multicast == nil {
		t.Fatal("Raft.Multicast = nil, want non-nil")
	}
	if got.Raft.Multicast.SharedSecret != "s3cret" {
		t.Errorf("Raft.Multicast.SharedSecret = %q, want %q", got.Raft.Multicast.SharedSecret, "s3cret")
	}
	if got.Raft.Multicast.MulticastGroup != "239.1.2.3:9876" {
		t.Errorf("Raft.Multicast.MulticastGroup = %q, want %q", got.Raft.Multicast.MulticastGroup, "239.1.2.3:9876")
	}
	if got.Barrier == nil {
		t.Fatal("Barrier = nil, want non-nil")
	}
	if !got.Barrier.Enabled {
		t.Error("Barrier.Enabled = false, want true")
	}
	if got.Barrier.Mode != "shamir" {
		t.Errorf("Barrier.Mode = %q, want %q", got.Barrier.Mode, "shamir")
	}
	if got.Barrier.SharesRequired != 3 {
		t.Errorf("Barrier.SharesRequired = %d, want %d", got.Barrier.SharesRequired, 3)
	}
	if got.Barrier.SharesTotal != 5 {
		t.Errorf("Barrier.SharesTotal = %d, want %d", got.Barrier.SharesTotal, 5)
	}
	if got.Connection == nil {
		t.Fatal("Connection = nil, want non-nil")
	}
	if got.Connection.Transport != "grpc" {
		t.Errorf("Connection.Transport = %q, want %q", got.Connection.Transport, "grpc")
	}
	if got.Connection.Address != "localhost:9090" {
		t.Errorf("Connection.Address = %q, want %q", got.Connection.Address, "localhost:9090")
	}
	if got.Connection.CAFile != "/etc/ca.pem" {
		t.Errorf("Connection.CAFile = %q, want %q", got.Connection.CAFile, "/etc/ca.pem")
	}
}

func TestDatabaseConfig_Validate_WithBarrierConfig(t *testing.T) {
	// Valid embedded config with barrier config should pass.
	cfg := DatabaseConfig{
		Name:    "barrier-db",
		Backend: BackendPebble,
		Mode:    ModeEmbedded,
		Barrier: &DatabaseBarrierConfig{
			Enabled:        true,
			Mode:           "shamir",
			SharesRequired: 3,
			SharesTotal:    5,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}

	// Barrier with passphrase mode should also pass.
	cfg.Barrier = &DatabaseBarrierConfig{
		Enabled: true,
		Mode:    "passphrase",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with passphrase barrier returned unexpected error: %v", err)
	}

	// Disabled barrier should also pass.
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with disabled barrier returned unexpected error: %v", err)
	}
}

func TestDatabaseRaftConfig_Discovery_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseRaftConfig
	}{
		{
			name: "static_discovery",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryStatic,
				Static: &StaticDiscoveryConfig{
					Peers:        []string{"node1:5000", "node2:5000"},
					SharedSecret: "s3cret",
				},
			},
		},
		{
			name: "multicast_discovery",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryMulticast,
				Multicast: &MulticastDiscoveryConfig{
					SharedSecret:     "cluster-key",
					MulticastGroup:   "239.1.2.3:9876",
					Interface:        "eth0",
					MinPeers:         3,
					AnnounceInterval: "5s",
					DiscoverTimeout:  "30s",
				},
			},
		},
		{
			name: "dns_discovery",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryDNS,
				DNS: &DNSDiscoveryConfig{
					Service:       "_raft",
					Proto:         "_tcp",
					Domain:        "cluster.example.com",
					LookupTimeout: "10s",
				},
			},
		},
		{
			name: "token_discovery",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftDistributed,
				Discovery:   DiscoveryToken,
				Token: &TokenDiscoveryConfig{
					Endpoint:    "https://discovery.example.com",
					Token:       "join-token-abc",
					ClusterSize: 5,
					Timeout:     "30s",
				},
			},
		},
		{
			name: "no_discovery_fields",
			cfg: DatabaseRaftConfig{
				ClusterMode: RaftNone,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.cfg)
			if err != nil {
				t.Fatalf("Marshal() returned unexpected error: %v", err)
			}

			var got DatabaseRaftConfig
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() returned unexpected error: %v", err)
			}

			if got.ClusterMode != tt.cfg.ClusterMode {
				t.Errorf("ClusterMode = %q, want %q", got.ClusterMode, tt.cfg.ClusterMode)
			}
			if got.Discovery != tt.cfg.Discovery {
				t.Errorf("Discovery = %q, want %q", got.Discovery, tt.cfg.Discovery)
			}

			// Verify nested discovery configs round-trip correctly.
			if tt.cfg.Static != nil {
				if got.Static == nil {
					t.Fatal("Static = nil, want non-nil")
				}
				if len(got.Static.Peers) != len(tt.cfg.Static.Peers) {
					t.Errorf("Static.Peers length = %d, want %d", len(got.Static.Peers), len(tt.cfg.Static.Peers))
				}
				if got.Static.SharedSecret != tt.cfg.Static.SharedSecret {
					t.Errorf("Static.SharedSecret = %q, want %q", got.Static.SharedSecret, tt.cfg.Static.SharedSecret)
				}
			}
			if tt.cfg.Multicast != nil {
				if got.Multicast == nil {
					t.Fatal("Multicast = nil, want non-nil")
				}
				if got.Multicast.SharedSecret != tt.cfg.Multicast.SharedSecret {
					t.Errorf("Multicast.SharedSecret = %q, want %q", got.Multicast.SharedSecret, tt.cfg.Multicast.SharedSecret)
				}
				if got.Multicast.MulticastGroup != tt.cfg.Multicast.MulticastGroup {
					t.Errorf("Multicast.MulticastGroup = %q, want %q", got.Multicast.MulticastGroup, tt.cfg.Multicast.MulticastGroup)
				}
				if got.Multicast.Interface != tt.cfg.Multicast.Interface {
					t.Errorf("Multicast.Interface = %q, want %q", got.Multicast.Interface, tt.cfg.Multicast.Interface)
				}
				if got.Multicast.MinPeers != tt.cfg.Multicast.MinPeers {
					t.Errorf("Multicast.MinPeers = %d, want %d", got.Multicast.MinPeers, tt.cfg.Multicast.MinPeers)
				}
			}
			if tt.cfg.DNS != nil {
				if got.DNS == nil {
					t.Fatal("DNS = nil, want non-nil")
				}
				if got.DNS.Domain != tt.cfg.DNS.Domain {
					t.Errorf("DNS.Domain = %q, want %q", got.DNS.Domain, tt.cfg.DNS.Domain)
				}
				if got.DNS.Service != tt.cfg.DNS.Service {
					t.Errorf("DNS.Service = %q, want %q", got.DNS.Service, tt.cfg.DNS.Service)
				}
				if got.DNS.Proto != tt.cfg.DNS.Proto {
					t.Errorf("DNS.Proto = %q, want %q", got.DNS.Proto, tt.cfg.DNS.Proto)
				}
			}
			if tt.cfg.Token != nil {
				if got.Token == nil {
					t.Fatal("Token = nil, want non-nil")
				}
				if got.Token.Endpoint != tt.cfg.Token.Endpoint {
					t.Errorf("Token.Endpoint = %q, want %q", got.Token.Endpoint, tt.cfg.Token.Endpoint)
				}
				if got.Token.Token != tt.cfg.Token.Token {
					t.Errorf("Token.Token = %q, want %q", got.Token.Token, tt.cfg.Token.Token)
				}
				if got.Token.ClusterSize != tt.cfg.Token.ClusterSize {
					t.Errorf("Token.ClusterSize = %d, want %d", got.Token.ClusterSize, tt.cfg.Token.ClusterSize)
				}
			}
		})
	}
}

func TestDatabaseConfig_TenantID_ValidatesWithTenantID(t *testing.T) {
	cfg := DatabaseConfig{
		TenantID: "tenant-xyz",
		Name:     "test",
		Backend:  BackendMemory,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with TenantID error = %v", err)
	}
}

func TestDatabaseConfig_TenantID_EmptyIsValid(t *testing.T) {
	cfg := DatabaseConfig{
		TenantID: "",
		Name:     "test",
		Backend:  BackendMemory,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with empty TenantID error = %v", err)
	}
}

func TestStatusSealed_Value(t *testing.T) {
	if StatusSealed != StatusType("sealed") {
		t.Errorf("StatusSealed = %q, want %q", StatusSealed, "sealed")
	}
}

func TestStatusSealed_DistinctFromOtherStatuses(t *testing.T) {
	if StatusSealed == StatusOpen {
		t.Error("StatusSealed should not equal StatusOpen")
	}
	if StatusSealed == StatusClosed {
		t.Error("StatusSealed should not equal StatusClosed")
	}
	if StatusSealed == StatusError {
		t.Error("StatusSealed should not equal StatusError")
	}
}

func TestDatabaseMeta_TenantID_JSONRoundTrip(t *testing.T) {
	meta := DatabaseMeta{
		TenantID: "tenant-abc",
		Name:     "tenant-db",
		Backend:  BackendMemory,
		Mode:     ModeEmbedded,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got DatabaseMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.TenantID != "tenant-abc" {
		t.Errorf("TenantID = %q, want %q", got.TenantID, "tenant-abc")
	}
}

func TestDatabaseMeta_TenantID_OmittedWhenEmpty(t *testing.T) {
	meta := DatabaseMeta{
		Name:    "no-tenant-db",
		Backend: BackendMemory,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	content := string(data)
	if containsSubstring(content, "tenant_id") {
		t.Errorf("meta JSON should omit tenant_id when empty, got: %s", content)
	}
}

func TestDatabaseInfo_TenantID_JSONRoundTrip(t *testing.T) {
	info := DatabaseInfo{
		TenantID: "tenant-info-abc",
		Name:     "info-db",
		Backend:  BackendMemory,
		Status:   StatusOpen,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got DatabaseInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.TenantID != "tenant-info-abc" {
		t.Errorf("TenantID = %q, want %q", got.TenantID, "tenant-info-abc")
	}
}

// ---------------------------------------------------------------------------
// DatabaseConfig.HasDedicatedStore
// ---------------------------------------------------------------------------

func TestDatabaseConfig_HasDedicatedStore_DefaultPathIsShared(t *testing.T) {
	cfg := DatabaseConfig{Name: "db", Backend: BackendPebble}
	if cfg.HasDedicatedStore() {
		t.Error("HasDedicatedStore() = true for empty DataDir + non-raft backend, want false")
	}
}

func TestDatabaseConfig_HasDedicatedStore_ExplicitDataDir(t *testing.T) {
	cfg := DatabaseConfig{Name: "db", Backend: BackendPebble, DataDir: "/data/tenant/db"}
	if !cfg.HasDedicatedStore() {
		t.Error("HasDedicatedStore() = false with explicit DataDir, want true")
	}
}

func TestDatabaseConfig_HasDedicatedStore_WhitespaceOnlyDataDirIsShared(t *testing.T) {
	cfg := DatabaseConfig{Name: "db", Backend: BackendPebble, DataDir: "   "}
	if cfg.HasDedicatedStore() {
		t.Error("HasDedicatedStore() = true for whitespace-only DataDir, want false")
	}
}

func TestDatabaseConfig_HasDedicatedStore_RaftBackend(t *testing.T) {
	cfg := DatabaseConfig{Name: "db", Backend: BackendRaft}
	if !cfg.HasDedicatedStore() {
		t.Error("HasDedicatedStore() = false for BackendRaft, want true")
	}
}

func TestDatabaseConfig_HasDedicatedStore_MemoryBackendNoDataDir(t *testing.T) {
	cfg := DatabaseConfig{Name: "db", Backend: BackendMemory}
	if cfg.HasDedicatedStore() {
		t.Error("HasDedicatedStore() = true for BackendMemory without DataDir, want false")
	}
}
