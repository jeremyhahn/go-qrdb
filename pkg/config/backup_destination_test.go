package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeBackupYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qrdb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupDestination_LoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeBackupYAML(t, "backup:\n  destination:\n    provider: local\n    settings:\n      path: "+dir+"\n      forcePathStyle: \"true\"\n    credentials:\n      key_file: /etc/qrdb/backup.key\n      secret_file: /etc/qrdb/backup.secret\n"))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	d := cfg.Backup.Destination
	if d == nil {
		t.Fatal("Backup.Destination = nil, want the block")
	}
	if d.Provider != "local" || d.Settings["path"] != dir {
		t.Fatalf("Backup.Destination = %+v, want provider local at %s", d, dir)
	}
	if d.Settings["forcePathStyle"] != "true" {
		t.Fatalf("settings = %v, want the provider key case kept (forcePathStyle)", d.Settings)
	}
	want := ObjstoreCredentialsConfig{KeyFile: "/etc/qrdb/backup.key", SecretFile: "/etc/qrdb/backup.secret"}
	if d.Credentials != want {
		t.Fatalf("Credentials = %+v, want %+v", d.Credentials, want)
	}
}

func TestBackupDestination_RefusesAnEmptyProvider(t *testing.T) {
	_, err := Load(writeBackupYAML(t, "backup:\n  destination:\n    settings:\n      path: /var/backups/qrdb\n"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "backup.destination.provider" {
		t.Fatalf("Load() = %v, want a ValidationError on backup.destination.provider", err)
	}
}

func TestBackupDestination_RefusesAKeyWithoutASecret(t *testing.T) {
	_, err := Load(writeBackupYAML(t, "backup:\n  destination:\n    provider: s3\n    credentials:\n      key_file: /etc/qrdb/backup.key\n"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "backup.destination.credentials.secret_file" {
		t.Fatalf("Load() = %v, want a ValidationError on backup.destination.credentials.secret_file", err)
	}
}

func TestBackupDestination_UnsetWithoutTheBlock(t *testing.T) {
	cfg, err := Load(writeBackupYAML(t, "storage:\n  backend: memory\n"))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Backup.Destination != nil {
		t.Fatalf("Backup.Destination = %+v, want nil", cfg.Backup.Destination)
	}
}
