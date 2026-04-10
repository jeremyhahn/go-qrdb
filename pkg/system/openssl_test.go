// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOpenSSLConfig(t *testing.T) {
	// This test parses the system's actual openssl.cnf if it exists.
	// On systems without OpenSSL installed, it returns nil without error.
	cfg, err := LoadOpenSSLConfig()
	if err != nil {
		t.Fatalf("LoadOpenSSLConfig returned error: %v", err)
	}

	if cfg != nil {
		t.Logf("Loaded system OpenSSL config: Country=%q Province=%q Locality=%q Organization=%q OU=%q CN=%q",
			cfg.Country, cfg.Province, cfg.Locality, cfg.Organization, cfg.OrganizationalUnit, cfg.CommonName)
	} else {
		t.Log("No system OpenSSL config found (expected on some systems)")
	}
}

func TestLoadOpenSSLConfig_NoConfigFound(t *testing.T) {
	// Override env to point to a nonexistent file and ensure no standard
	// paths are used by temporarily setting OPENSSL_CONF to empty.
	original := os.Getenv("OPENSSL_CONF")
	t.Setenv("OPENSSL_CONF", "/nonexistent/path/openssl.cnf")
	defer func() {
		if original != "" {
			_ = os.Setenv("OPENSSL_CONF", original)
		}
	}()

	// Save and clear standard paths
	origPaths := opensslConfigPaths
	opensslConfigPaths = []string{"/nonexistent/a", "/nonexistent/b"}
	defer func() { opensslConfigPaths = origPaths }()

	cfg, err := LoadOpenSSLConfig()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config when no file exists")
	}
}

func TestParseOpenSSLConfig(t *testing.T) {
	content := `# OpenSSL test configuration
[ca]
default_ca = CA_default

[req]
default_bits = 2048

[req_distinguished_name]
countryName                     = Country Name (2 letter code)
countryName_default             = US
stateOrProvinceName             = State or Province Name (full name)
stateOrProvinceName_default     = "California"
localityName                    = Locality Name (eg, city)
localityName_default            = 'San Francisco'
0.organizationName              = Organization Name (eg, company)
0.organizationName_default      = Acme Corp
organizationalUnitName          = Organizational Unit Name
organizationalUnitName_default  = Engineering
commonName                      = Common Name (eg, YOUR name)
commonName_default              = test.example.com

[v3_ca]
basicConstraints = critical, CA:TRUE
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"Country", cfg.Country, "US"},
		{"Province", cfg.Province, "California"},
		{"Locality", cfg.Locality, "San Francisco"},
		{"Organization", cfg.Organization, "Acme Corp"},
		{"OrganizationalUnit", cfg.OrganizationalUnit, "Engineering"},
		{"CommonName", cfg.CommonName, "test.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.got)
			}
		})
	}
}

func TestParseOpenSSLConfig_InvalidPath(t *testing.T) {
	cfg, err := parseOpenSSLConfig("/nonexistent/openssl.cnf")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if cfg != nil {
		t.Fatal("expected nil config on error")
	}
}

func TestFindOpenSSLConfig_EnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "custom-openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte("[req]\n"), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	t.Setenv("OPENSSL_CONF", tmpFile)

	path := findOpenSSLConfig()
	if path != tmpFile {
		t.Errorf("expected %q, got %q", tmpFile, path)
	}
}

func TestFindOpenSSLConfig_EnvVarNonexistent(t *testing.T) {
	t.Setenv("OPENSSL_CONF", "/nonexistent/openssl.cnf")

	// Save and clear standard paths so we get empty string
	origPaths := opensslConfigPaths
	opensslConfigPaths = []string{"/nonexistent/a"}
	defer func() { opensslConfigPaths = origPaths }()

	path := findOpenSSLConfig()
	if path != "" {
		t.Errorf("expected empty string for nonexistent env path, got %q", path)
	}
}

func TestFindOpenSSLConfig_StandardPaths(t *testing.T) {
	// Unset env var so standard paths are checked
	t.Setenv("OPENSSL_CONF", "")

	path := findOpenSSLConfig()
	if path == "" {
		t.Log("No standard OpenSSL config path found on this system")
		return
	}

	// Verify the found path is one of the standard paths
	found := false
	for _, p := range opensslConfigPaths {
		if path == p {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("found path %q is not in standard paths", path)
	}
}

func TestParseKeyValue(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{
			name:      "simple key=value",
			input:     "key = value",
			wantKey:   "key",
			wantValue: "value",
			wantOK:    true,
		},
		{
			name:      "no spaces",
			input:     "key=value",
			wantKey:   "key",
			wantValue: "value",
			wantOK:    true,
		},
		{
			name:      "extra spaces",
			input:     "  key  =  value  ",
			wantKey:   "key",
			wantValue: "value",
			wantOK:    true,
		},
		{
			name:      "no equals sign",
			input:     "no equals here",
			wantKey:   "",
			wantValue: "",
			wantOK:    false,
		},
		{
			name:      "empty value",
			input:     "key = ",
			wantKey:   "key",
			wantValue: "",
			wantOK:    true,
		},
		{
			name:      "value with equals",
			input:     "key = a=b",
			wantKey:   "key",
			wantValue: "a=b",
			wantOK:    true,
		},
		{
			name:      "value with inline hash comment",
			input:     "key = value # comment",
			wantKey:   "key",
			wantValue: "value",
			wantOK:    true,
		},
		{
			name:      "value with inline semicolon comment",
			input:     "key = value ; comment",
			wantKey:   "key",
			wantValue: "value",
			wantOK:    true,
		},
		{
			name:      "quoted value preserves hash",
			input:     `key = "value # not a comment"`,
			wantKey:   "key",
			wantValue: `"value # not a comment"`,
			wantOK:    true,
		},
		{
			name:      "dotted key",
			input:     "0.organizationName_default = Acme",
			wantKey:   "0.organizationName_default",
			wantValue: "Acme",
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, ok := parseKeyValue(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ok: expected %v, got %v", tt.wantOK, ok)
			}
			if key != tt.wantKey {
				t.Errorf("key: expected %q, got %q", tt.wantKey, key)
			}
			if value != tt.wantValue {
				t.Errorf("value: expected %q, got %q", tt.wantValue, value)
			}
		})
	}
}

func TestStripQuotes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"double quotes", `"hello"`, "hello"},
		{"single quotes", `'hello'`, "hello"},
		{"no quotes", "hello", "hello"},
		{"mismatched quotes", `"hello'`, `"hello'`},
		{"empty string", "", ""},
		{"single char", "x", "x"},
		{"only quotes", `""`, ""},
		{"single only quotes", `''`, ""},
		{"spaces around double", `  "hello"  `, "hello"},
		{"spaces around single", `  'hello'  `, "hello"},
		{"inner quotes preserved", `"it's here"`, "it's here"},
		{"nested double in single", `'"hello"'`, `"hello"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripQuotes(tt.input)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestRemoveInlineComment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no comment", "value", "value"},
		{"hash comment", "value # comment", "value"},
		{"semicolon comment", "value ; comment", "value"},
		{"hash in double quotes", `"value # here"`, `"value # here"`},
		{"hash in single quotes", `'value # here'`, `'value # here'`},
		{"semicolon in double quotes", `"value ; here"`, `"value ; here"`},
		{"comment after quoted", `"value" # comment`, `"value"`},
		{"empty string", "", ""},
		{"only comment", "# comment", ""},
		{"multiple hashes", "val # one # two", "val"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeInlineComment(tt.input)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestParseOpenSSLConfig_ContinuationLines(t *testing.T) {
	content := `[req_distinguished_name]
countryName_default = U\
S
stateOrProvinceName_default = New \
York
0.organizationName_default = Multi \
Line \
Corp
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"Country continuation", cfg.Country, "US"},
		{"Province continuation", cfg.Province, "New York"},
		{"Organization multi-line", cfg.Organization, "Multi Line Corp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.got)
			}
		})
	}
}

func TestParseOpenSSLConfig_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	if cfg.Country != "" || cfg.Province != "" || cfg.Locality != "" ||
		cfg.Organization != "" || cfg.OrganizationalUnit != "" || cfg.CommonName != "" {
		t.Error("expected all empty fields for empty file")
	}
}

func TestParseOpenSSLConfig_NoReqSection(t *testing.T) {
	content := `[ca]
default_ca = CA_default

[CA_default]
dir = /etc/pki/CA

[req]
default_bits = 2048
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	if cfg.Country != "" || cfg.Province != "" || cfg.Locality != "" ||
		cfg.Organization != "" || cfg.OrganizationalUnit != "" || cfg.CommonName != "" {
		t.Error("expected all empty fields when no req_distinguished_name section exists")
	}
}

func TestParseOpenSSLConfig_CommentsAndBlankLines(t *testing.T) {
	content := `# This is a comment
; This is also a comment

[req_distinguished_name]
# Comment inside section
countryName_default = US
; Another comment
stateOrProvinceName_default = Texas

`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	if cfg.Country != "US" {
		t.Errorf("Country: expected %q, got %q", "US", cfg.Country)
	}
	if cfg.Province != "Texas" {
		t.Errorf("Province: expected %q, got %q", "Texas", cfg.Province)
	}
}

func TestParseOpenSSLConfig_SectionAfterReq(t *testing.T) {
	// Ensure fields in sections after req_distinguished_name are not captured
	content := `[req_distinguished_name]
countryName_default = US

[v3_ca]
countryName_default = GB
stateOrProvinceName_default = England
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	if cfg.Country != "US" {
		t.Errorf("Country: expected %q, got %q (should not pick up v3_ca value)", "US", cfg.Country)
	}
	if cfg.Province != "" {
		t.Errorf("Province: expected empty (only in v3_ca section), got %q", cfg.Province)
	}
}

func TestParseOpenSSLConfig_CaseInsensitiveKeys(t *testing.T) {
	content := `[req_distinguished_name]
COUNTRYNAME_DEFAULT = US
StateOrProvinceName_Default = Oregon
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "openssl.cnf")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := parseOpenSSLConfig(tmpFile)
	if err != nil {
		t.Fatalf("parseOpenSSLConfig returned error: %v", err)
	}

	if cfg.Country != "US" {
		t.Errorf("Country: expected %q, got %q", "US", cfg.Country)
	}
	if cfg.Province != "Oregon" {
		t.Errorf("Province: expected %q, got %q", "Oregon", cfg.Province)
	}
}
