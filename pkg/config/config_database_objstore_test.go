package config

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

// databaseObjstoreYAML is a daemon config declaring one `backend: objstore`
// database with its own provider block.
const databaseObjstoreYAML = `databases:
  - name: blobs
    backend: objstore
    objstore:
      provider: s3
      settings:
        bucket: "blobs-bucket"
        forcePathStyle: "true"
      credentials:
        key_file: /run/secrets/key
        secret_file: /run/secrets/secret
`

func TestLoad_DatabaseObjstoreBlock(t *testing.T) {
	cfg, err := Load(writeTemp(t, databaseObjstoreYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Databases) != 1 {
		t.Fatalf("Databases: got %d entries, want 1", len(cfg.Databases))
	}
	got := cfg.Databases[0].Objstore
	if got == nil {
		t.Fatal("Databases[0].Objstore: got nil, want the YAML block")
	}
	if got.Provider != "s3" {
		t.Errorf("provider = %q, want s3", got.Provider)
	}
	// Provider setting names are case-sensitive; the loader must not fold them.
	wantSettings := map[string]string{"bucket": "blobs-bucket", "forcePathStyle": "true"}
	if !maps.Equal(got.Settings, wantSettings) {
		t.Errorf("settings = %v, want %v", got.Settings, wantSettings)
	}
	wantCreds := ObjstoreCredentialsConfig{KeyFile: "/run/secrets/key", SecretFile: "/run/secrets/secret"}
	if got.Credentials != wantCreds {
		t.Errorf("credentials = %+v, want %+v", got.Credentials, wantCreds)
	}
}

func TestLoad_DatabaseObjstoreRefusesAValueUnderCredentials(t *testing.T) {
	yaml := strings.Replace(databaseObjstoreYAML, "        secret_file: /run/secrets/secret\n",
		"        secret_file: /run/secrets/secret\n        secret_key: s3cr3t-value\n", 1)
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("Load accepted an inline value under databases[].objstore.credentials")
	}
	if strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatalf("Load error echoes the secret: %v", err)
	}
}

func TestValidate_DatabaseObjstoreBlock(t *testing.T) {
	block := func() *ObjstoreConfig {
		return &ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": "/srv/blobs"}}
	}
	tests := []struct {
		name      string
		db        DatabaseConfig
		wantField string // a *ValidationError naming this field; "" = *DatabaseConfigError
		ok        bool
	}{
		{name: "objstore with block", db: DatabaseConfig{Name: "blobs", Backend: "objstore", Objstore: block()}, ok: true},
		{name: "objstore without block", db: DatabaseConfig{Name: "blobs", Backend: "objstore"}},
		{name: "objstore without provider", db: DatabaseConfig{Name: "blobs", Backend: "objstore", Objstore: &ObjstoreConfig{}}},
		{name: "block on another backend", db: DatabaseConfig{Name: "blobs", Backend: "pebble", Objstore: block()}},
		{
			name: "key file without secret file",
			db: DatabaseConfig{Name: "blobs", Backend: "objstore", Objstore: &ObjstoreConfig{
				Provider: "s3", Credentials: ObjstoreCredentialsConfig{KeyFile: "/run/secrets/key"},
			}},
			wantField: "databases[0].objstore.credentials.secret_file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Databases = []DatabaseConfig{tc.db}
			err := cfg.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if tc.wantField != "" {
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.Field != tc.wantField {
					t.Fatalf("Validate() = %v, want a ValidationError on %s", err, tc.wantField)
				}
				return
			}
			var de *DatabaseConfigError
			if !errors.As(err, &de) || de.Name != "blobs" {
				t.Fatalf("Validate() = %v, want a DatabaseConfigError naming blobs", err)
			}
		})
	}
}
