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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestTLSConfig_AdditionalCAs_YAMLRoundTrip ensures the AdditionalCAs
// field is bound to the additional_cas YAML key and survives a round
// trip without loss.
func TestTLSConfig_AdditionalCAs_YAMLRoundTrip(t *testing.T) {
	tc := TLSConfig{
		AdditionalCAs: []string{"/etc/ca/one.pem", "/etc/ca/two.pem"},
	}

	data, err := yaml.Marshal(&tc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "additional_cas:") {
		t.Fatalf("expected additional_cas key in YAML, got %s", string(data))
	}

	var got TLSConfig
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.AdditionalCAs) != 2 {
		t.Fatalf("expected 2 additional CAs, got %d", len(got.AdditionalCAs))
	}
	if got.AdditionalCAs[0] != "/etc/ca/one.pem" || got.AdditionalCAs[1] != "/etc/ca/two.pem" {
		t.Fatalf("unexpected additional CAs: %v", got.AdditionalCAs)
	}
}

// TestMTLSConfig_ClientCAFiles_YAMLRoundTrip ensures the ClientCAFiles
// field is bound to the client_ca_files YAML key under the mtls
// section.
func TestMTLSConfig_ClientCAFiles_YAMLRoundTrip(t *testing.T) {
	cfg := Config{
		MTLS: MTLSConfig{
			ClientCAFiles: []string{"/etc/mtls/clients.pem"},
		},
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "client_ca_files:") {
		t.Fatalf("expected client_ca_files key in YAML, got %s", string(data))
	}
	if !strings.Contains(string(data), "mtls:") {
		t.Fatalf("expected mtls key in YAML, got %s", string(data))
	}

	var got Config
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.MTLS.ClientCAFiles) != 1 {
		t.Fatalf("expected 1 client CA file, got %d", len(got.MTLS.ClientCAFiles))
	}
	if got.MTLS.ClientCAFiles[0] != "/etc/mtls/clients.pem" {
		t.Fatalf("unexpected client CA files: %v", got.MTLS.ClientCAFiles)
	}
}

// TestConfig_LoadYAML_TLSPoolFields verifies that a YAML file with the
// new pool fields populates the Config struct correctly via the public
// Load helper.
func TestConfig_LoadYAML_TLSPoolFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qrdbd.yaml")

	yamlBody := `
server:
  admin_port: 63002
  grpc_port: 63003
  quic_port: 63004
  host: 127.0.0.1
raft:
  data_dir: ./data
  shard_id: 1
  replica_id: 1
  raft_address: 127.0.0.1:63001
  peers: 1=127.0.0.1:63001
tls:
  enabled: true
  additional_cas:
    - /etc/qrdb/extra1.pem
    - /etc/qrdb/extra2.pem
mtls:
  client_ca_files:
    - /etc/qrdb/clients.pem
`

	if err := os.WriteFile(path, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TLS.AdditionalCAs) != 2 {
		t.Fatalf("expected 2 additional CAs, got %d", len(cfg.TLS.AdditionalCAs))
	}
	if cfg.TLS.AdditionalCAs[0] != "/etc/qrdb/extra1.pem" {
		t.Fatalf("unexpected first additional CA: %s", cfg.TLS.AdditionalCAs[0])
	}
	if len(cfg.MTLS.ClientCAFiles) != 1 {
		t.Fatalf("expected 1 client CA file, got %d", len(cfg.MTLS.ClientCAFiles))
	}
	if cfg.MTLS.ClientCAFiles[0] != "/etc/qrdb/clients.pem" {
		t.Fatalf("unexpected client CA file: %s", cfg.MTLS.ClientCAFiles[0])
	}
}
