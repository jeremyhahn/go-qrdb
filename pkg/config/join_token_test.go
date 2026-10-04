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
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// testJoinToken is long enough to clear MinJoinTokenLength, standing in for
// a real clusterenroll.MintJoinToken value in tests that do not need the
// token to be cryptographically real, only long enough.
const testJoinToken = "s3cret-token-well-over-the-floor"

func TestResolveJoinToken_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  "+testJoinToken+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ResolveJoinToken(path, "")
	if err != nil {
		t.Fatalf("ResolveJoinToken: %v", err)
	}
	if got != testJoinToken {
		t.Fatalf("token = %q, want %q (trimmed)", got, testJoinToken)
	}
}

func TestResolveJoinToken_FromEnv(t *testing.T) {
	t.Setenv("QRDB_TEST_JOIN_TOKEN", testJoinToken)
	got, err := ResolveJoinToken("", "QRDB_TEST_JOIN_TOKEN")
	if err != nil {
		t.Fatalf("ResolveJoinToken: %v", err)
	}
	if got != testJoinToken {
		t.Fatalf("token = %q, want %q", got, testJoinToken)
	}
}

func TestResolveJoinToken_FileTooShortRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ResolveJoinToken(path, "")
	var tooShort *JoinTokenTooShortError
	if !errors.As(err, &tooShort) {
		t.Fatalf("ResolveJoinToken with a short file = %v, want *JoinTokenTooShortError", err)
	}
}

func TestResolveJoinToken_EnvTooShortRefused(t *testing.T) {
	t.Setenv("QRDB_TEST_JOIN_TOKEN_SHORT", "short")
	_, err := ResolveJoinToken("", "QRDB_TEST_JOIN_TOKEN_SHORT")
	var tooShort *JoinTokenTooShortError
	if !errors.As(err, &tooShort) {
		t.Fatalf("ResolveJoinToken with a short env value = %v, want *JoinTokenTooShortError", err)
	}
}

func TestResolveJoinToken_GroupReadableFileRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes do not apply on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(testJoinToken), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ResolveJoinToken(path, "")
	var modeErr *JoinTokenFileModeError
	if !errors.As(err, &modeErr) {
		t.Fatalf("ResolveJoinToken with a group-readable file = %v, want *JoinTokenFileModeError", err)
	}
}

func TestResolveJoinToken_WorldReadableFileRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes do not apply on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(testJoinToken), 0o604); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ResolveJoinToken(path, "")
	var modeErr *JoinTokenFileModeError
	if !errors.As(err, &modeErr) {
		t.Fatalf("ResolveJoinToken with a world-readable file = %v, want *JoinTokenFileModeError", err)
	}
}

func TestResolveJoinToken_BothConfiguredRefused(t *testing.T) {
	_, err := ResolveJoinToken("some/path", "SOME_ENV")
	var conflict *JoinTokenSourceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ResolveJoinToken(file, env) = %v, want *JoinTokenSourceConflictError", err)
	}
}

func TestResolveJoinToken_NeitherConfigured(t *testing.T) {
	_, err := ResolveJoinToken("", "")
	var notConfigured *JoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("ResolveJoinToken(\"\", \"\") = %v, want *JoinTokenNotConfiguredError", err)
	}
}

func TestResolveJoinToken_FileMissing(t *testing.T) {
	_, err := ResolveJoinToken(filepath.Join(t.TempDir(), "nope"), "")
	var fileErr *JoinTokenFileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("ResolveJoinToken with a missing file = %v, want *JoinTokenFileError", err)
	}
}

func TestResolveJoinToken_FileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ResolveJoinToken(path, "")
	var emptyErr *JoinTokenEmptyError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("ResolveJoinToken with a blank file = %v, want *JoinTokenEmptyError", err)
	}
}

func TestResolveJoinToken_EnvEmpty(t *testing.T) {
	t.Setenv("QRDB_TEST_JOIN_TOKEN_EMPTY", "")
	_, err := ResolveJoinToken("", "QRDB_TEST_JOIN_TOKEN_EMPTY")
	var emptyErr *JoinTokenEmptyError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("ResolveJoinToken with an unset env = %v, want *JoinTokenEmptyError", err)
	}
}

func TestJoinTokenErrors_ErrorStrings(t *testing.T) {
	tests := []error{
		&JoinTokenSourceConflictError{},
		&JoinTokenNotConfiguredError{},
		&JoinTokenFileError{Path: "/tmp/x", Err: errors.New("boom")},
		&JoinTokenEmptyError{Source: "file:/tmp/x"},
		&JoinTokenFileModeError{Path: "/tmp/x", Mode: 0o640},
		&JoinTokenFileOwnerError{Path: "/tmp/x"},
		&JoinTokenTooShortError{Source: "file:/tmp/x", Length: 5},
	}
	for _, err := range tests {
		if err.Error() == "" {
			t.Errorf("%T.Error() is empty", err)
		}
	}
	wrapped := &JoinTokenFileError{Path: "/tmp/x", Err: errors.New("boom")}
	if wrapped.Unwrap() == nil {
		t.Fatal("JoinTokenFileError.Unwrap() is nil")
	}
}

func TestValidate_ClusterJoinAndBootstrapJoin(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:    "cluster join disabled by default is valid",
			mutate:  func(c *Config) {},
			wantErr: false,
		},
		{
			name: "cluster join enabled with no host",
			mutate: func(c *Config) {
				c.ClusterJoin.Enabled = true
				c.Seal.Enabled = true
			},
			wantErr: true,
		},
		{
			name: "cluster join enabled and fully configured",
			mutate: func(c *Config) {
				c.ClusterJoin.Enabled = true
				c.ClusterJoin.ListenHost = "127.0.0.1"
				c.Seal.Enabled = true
			},
			wantErr: false,
		},
		{
			name: "cluster join enabled with seal disabled",
			mutate: func(c *Config) {
				c.ClusterJoin.Enabled = true
				c.ClusterJoin.ListenHost = "127.0.0.1"
				// Seal.Enabled now defaults to true (go-qrdb#548: `qrdb
				// init` needs the system barrier up even under strategy
				// "none"), so "disabled" must be set explicitly here --
				// member-side CSR signing has no meaning with no barrier
				// to gate Key A behind.
				c.Seal.Enabled = false
			},
			wantErr: true,
		},
		{
			name: "cluster join listen port out of range",
			mutate: func(c *Config) {
				c.ClusterJoin.Enabled = true
				c.ClusterJoin.ListenHost = "127.0.0.1"
				c.Seal.Enabled = true
				c.ClusterJoin.ListenPort = 70000
			},
			wantErr: true,
		},
		{
			name: "bootstrap join addr without key",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
				c.Bootstrap.JoinTokenFile = "/tmp/tok"
			},
			wantErr: true,
		},
		{
			name: "bootstrap join key without addr",
			mutate: func(c *Config) {
				c.Bootstrap.JoinKey = "aa"
				c.Bootstrap.JoinTokenFile = "/tmp/tok"
			},
			wantErr: true,
		},
		{
			name: "bootstrap join configured with no token source",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
				c.Bootstrap.JoinKey = "aa"
			},
			wantErr: true,
		},
		{
			name: "bootstrap join fully configured",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
				c.Bootstrap.JoinKey = "aa"
				c.Bootstrap.JoinTokenFile = "/tmp/tok"
			},
			wantErr: false,
		},
		{
			name: "bootstrap join token file and env both set",
			mutate: func(c *Config) {
				c.Bootstrap.JoinTokenFile = "/tmp/tok"
				c.Bootstrap.JoinTokenEnv = "SOME_ENV"
			},
			wantErr: true,
		},
		{
			name: "bootstrap join http url without https is refused",
			mutate: func(c *Config) {
				c.Bootstrap.JoinHTTPURL = "http://member1.example.com:8443"
				c.Bootstrap.JoinTokenFile = "/tmp/tok"
			},
			wantErr: true,
		},
		{
			name: "bootstrap join http url with https is valid",
			mutate: func(c *Config) {
				c.Bootstrap.JoinHTTPURL = "https://member1.example.com:8443"
				c.Bootstrap.JoinTokenFile = "/tmp/tok"
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if tt.wantErr {
				var verr *ValidationError
				if !errors.As(err, &verr) {
					t.Fatalf("Validate() error = %T, want *ValidationError", err)
				}
			}
		})
	}
}
