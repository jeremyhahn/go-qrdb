// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"strings"
	"testing"
)

const fakeObjstoreSecret = "fake-secret-access-key-for-tests"

func TestObjstoreConfigValidate_AcceptsFileReferences(t *testing.T) {
	for name, creds := range map[string]ObjstoreCredentialsConfig{
		"none":           {},
		"secret only":    {SecretFile: "/run/secrets/account-key"},
		"key and secret": {KeyFile: "/run/secrets/key", SecretFile: "/run/secrets/secret"},
	} {
		c := ObjstoreConfig{Provider: "s3", Settings: map[string]string{"bucket": "b"}, Credentials: creds}
		if err := c.validate("storage.objstore"); err != nil {
			t.Errorf("%s: validate() = %v", name, err)
		}
	}
}

func TestObjstoreConfigValidate_RefusesAKeyWithoutASecret(t *testing.T) {
	c := ObjstoreConfig{Credentials: ObjstoreCredentialsConfig{KeyFile: "/run/secrets/key"}}
	var ve *ValidationError
	if err := c.validate("storage.objstore"); !errors.As(err, &ve) || ve.Field != "storage.objstore.credentials.secret_file" {
		t.Fatalf("validate() = %v, want a ValidationError on storage.objstore.credentials.secret_file", err)
	}
}

func TestValidate_ChecksTheObjstoreCredential(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Storage.Objstore.Credentials = ObjstoreCredentialsConfig{KeyFile: "/run/secrets/key"}
	var ve *ValidationError
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "storage.objstore.credentials.secret_file" {
		t.Fatalf("Validate() = %v, want a ValidationError on storage.objstore.credentials.secret_file", err)
	}
	cfg.Storage.Objstore.Credentials = ObjstoreCredentialsConfig{KeyFile: "/run/secrets/key", SecretFile: "/run/secrets/secret"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with file references = %v", err)
	}
}

// writeObjstoreYAML writes a configuration file whose storage section is body.
func writeObjstoreYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qrdb.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  backend: objstore\n  objstore:\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_ObjstoreCredentialReferences(t *testing.T) {
	path := writeObjstoreYAML(t, "    provider: memory\n    credentials:\n      key_file: /run/secrets/key\n      secret_file: /run/secrets/secret\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	want := ObjstoreCredentialsConfig{KeyFile: "/run/secrets/key", SecretFile: "/run/secrets/secret"}
	if cfg.Storage.Objstore.Credentials != want {
		t.Fatalf("credentials = %+v, want %+v", cfg.Storage.Objstore.Credentials, want)
	}
}

func TestLoad_RefusesAValueUnderObjstoreCredentials(t *testing.T) {
	for _, key := range []string{"secret", "access_key"} {
		t.Run(key, func(t *testing.T) {
			_, err := Load(writeObjstoreYAML(t, "    provider: memory\n    credentials:\n      "+key+": "+fakeObjstoreSecret+"\n"))
			var unknown *UnknownFieldError
			if !errors.As(err, &unknown) || unknown.Path != "storage.objstore.credentials."+key {
				t.Fatalf("Load() = %v, want an UnknownFieldError on storage.objstore.credentials.%s", err, key)
			}
			if strings.Contains(err.Error(), fakeObjstoreSecret) {
				t.Fatal("the error carries the credential")
			}
		})
	}
}
