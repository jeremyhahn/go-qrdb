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
	"maps"
	"strings"
	"testing"
)

func TestLoad_VolumePoolOptionsKeepKeyCase(t *testing.T) {
	path := writeTemp(t, `
storage:
  volume_pools:
    - name: "a"
      path: "/mnt/a"
      provider: "dir"
    - name: "b"
      path: "/mnt/b"
      provider: "dir"
      options:
        mountFlags: "ro"
        size: "1G"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(cfg.Storage.VolumePools[0].Options) != 0 {
		t.Fatalf("pool a options = %v, want none", cfg.Storage.VolumePools[0].Options)
	}
	want := map[string]string{"mountFlags": "ro", "size": "1G"}
	if !maps.Equal(cfg.Storage.VolumePools[1].Options, want) {
		t.Fatalf("pool b options = %v, want %v", cfg.Storage.VolumePools[1].Options, want)
	}
}

func TestLoad_VolumePoolOptionsCaseCollisionRefused(t *testing.T) {
	path := writeTemp(t, `
storage:
  volume_pools:
    - name: "a"
      path: "/mnt/a"
      provider: "dir"
      options:
        mountFlags: "ro"
        MountFlags: "rw"
`)
	_, err := Load(path)
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load() = %v, want *LoadError", err)
	}
	for _, s := range []string{"storage.volume_pools[0].options", "mountFlags", "MountFlags"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("Load() error %q does not name %q", err, s)
		}
	}
}

func TestLoad_ObjstoreSettingsKeepKeyCase(t *testing.T) {
	path := writeObjstoreYAML(t, "    provider: memory\n    settings:\n      forcePathStyle: \"true\"\n      bucket: b\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	want := map[string]string{"forcePathStyle": "true", "bucket": "b"}
	if !maps.Equal(cfg.Storage.Objstore.Settings, want) {
		t.Fatalf("settings = %v, want %v", cfg.Storage.Objstore.Settings, want)
	}
}

func TestLoad_ObjstoreSettingsEnvOverrideKeepsKeyCase(t *testing.T) {
	t.Setenv("QRDB_STORAGE_OBJSTORE_SETTINGS_FORCEPATHSTYLE", "false")
	path := writeObjstoreYAML(t, "    provider: memory\n    settings:\n      forcePathStyle: \"true\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	want := map[string]string{"forcePathStyle": "false"}
	if !maps.Equal(cfg.Storage.Objstore.Settings, want) {
		t.Fatalf("settings = %v, want %v", cfg.Storage.Objstore.Settings, want)
	}
}

func TestLoad_ObjstoreSettingsCaseCollisionRefused(t *testing.T) {
	path := writeObjstoreYAML(t, "    provider: memory\n    settings:\n      forcePathStyle: \"true\"\n      forcepathstyle: \"false\"\n")
	_, err := Load(path)
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load() = %v, want *LoadError", err)
	}
	for _, s := range []string{"storage.objstore.settings", "forcePathStyle", "forcepathstyle"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("Load() error %q does not name %q", err, s)
		}
	}
}
