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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
)

// TestLoad_UnknownTopLevelKey_Refused is the RED/GREEN pair's red half for an
// unrecognized top-level section: before validateKnownFields existed, this
// file loaded cleanly and "bogus_top_level_key" had no effect whatsoever.
func TestLoad_UnknownTopLevelKey_Refused(t *testing.T) {
	path := writeTemp(t, `
bogus_top_level_key: true
server:
  admin_port: 8080
`)
	_, err := Load(path)
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("Load: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "bogus_top_level_key" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "bogus_top_level_key")
	}
	if ufe.File != path {
		t.Errorf("File: got %q, want %q", ufe.File, path)
	}
	if ufe.Line != 2 {
		t.Errorf("Line: got %d, want 2", ufe.Line)
	}
}

// TestLoad_UnknownNestedKey_Refused_NamesPath pins the owner's own
// motivating example: a documented-but-wrong "databases[].protocol.etcd"
// key must be refused, naming the exact indexed path.
func TestLoad_UnknownNestedKey_Refused_NamesPath(t *testing.T) {
	path := writeTemp(t, `
databases:
  - name: "k8s-prod"
    backend: "raft"
    protocol:
      etcd:
        enabled: true
`)
	_, err := Load(path)
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("Load: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "databases[0].protocol" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "databases[0].protocol")
	}
}

// TestLoad_MisspelledKey_Refused covers the ordinary typo case: a key one
// letter off from the real field name is unknown, not a fuzzy match.
func TestLoad_MisspelledKey_Refused(t *testing.T) {
	path := writeTemp(t, `
storage:
  backend: "pebble"
  cach_size: 1024
`)
	_, err := Load(path)
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("Load: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "storage.cach_size" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "storage.cach_size")
	}
}

// TestLoad_UnknownKeyUnderSequenceElement covers a nested-under-array
// misspelling distinct from the top-level and single-nested cases: the
// index must thread through a slice of structs.
func TestLoad_UnknownKeyUnderSequenceElement(t *testing.T) {
	path := writeTemp(t, `
storage:
  volume_pools:
    - name: "default"
      path: "/mnt/a"
      providerr: "dir"
`)
	_, err := Load(path)
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("Load: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "storage.volume_pools[0].providerr" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "storage.volume_pools[0].providerr")
	}
}

// TestLoad_CompiledOutFeatureKey_IsKnownNotUnknown is requirement #2: a key
// for a feature this binary may not have compiled in (protocols.mysql
// without build tag qrdb_mysql) must never be reported as unknown -- pkg/
// config declares every field unconditionally. Whether it is ultimately
// refused is Config.ValidateCapabilities's job, and the expectation there
// tracks whether this test binary carries the tag, exactly as
// TestValidateCapabilities_TagAbsentProtocol does for the non-strict path.
func TestLoad_CompiledOutFeatureKey_IsKnownNotUnknown(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		feat string
	}{
		{"mysql", "protocols:\n  mysql: true\n", "mysql"},
		{"postgresql", "protocols:\n  postgresql: true\n", "pgsql"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.yaml)
			cfg, err := Load(path)
			var ufe *UnknownFieldError
			if errors.As(err, &ufe) {
				t.Fatalf("Load: got *UnknownFieldError %v, want the key recognized", ufe)
			}
			if err != nil {
				t.Fatalf("Load: unexpected error: %v", err)
			}
			capErr := cfg.ValidateCapabilities()
			if capabilities.Has(tc.feat) {
				if capErr != nil {
					t.Fatalf("feature %s compiled in: ValidateCapabilities: want nil, got %v", tc.feat, capErr)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(capErr, &verr) {
				t.Fatalf("feature %s absent: ValidateCapabilities: got %v, want *ValidationError", tc.feat, capErr)
			}
		})
	}
}

// TestLoad_IntegrationExemplarConfigsLoadStrictly pins the two qrdbd
// configs the integration docker-compose stack mounts via QRDB_CONFIG /
// --config so they cannot silently drift from the schema the same way the
// configs/ exemplars are pinned in exemplar_configs_test.go.
func TestLoad_IntegrationExemplarConfigsLoadStrictly(t *testing.T) {
	for _, path := range []string{
		"../../test/integration/api/config.test.yaml",
		"../../test/integration/api/config.test.shamir.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// TestUnknownFieldError_ErrorString pins the message shape: it names the
// path, the file, and -- when known -- the line.
func TestUnknownFieldError_ErrorString(t *testing.T) {
	e := &UnknownFieldError{File: "/etc/qrdb/qrdb.yaml", Path: "databases[2].protocol", Line: 42}
	got := e.Error()
	want := `config: unknown key "databases[2].protocol" in /etc/qrdb/qrdb.yaml:42`
	if got != want {
		t.Errorf("Error(): got %q, want %q", got, want)
	}
}

// TestUnknownFieldError_ErrorString_NoLine covers the Line-unknown case
// (zero), which must omit the trailing ":<line>" rather than print ":0".
func TestUnknownFieldError_ErrorString_NoLine(t *testing.T) {
	e := &UnknownFieldError{File: "/etc/qrdb/qrdb.yaml", Path: "bogus"}
	got := e.Error()
	want := `config: unknown key "bogus" in /etc/qrdb/qrdb.yaml`
	if got != want {
		t.Errorf("Error(): got %q, want %q", got, want)
	}
}

// TestLoad_EmptyFile_StillLoads guards the empty-document fast path in
// validateKnownFields against a regression that would refuse the
// documented "an otherwise empty file is enough to turn the environment
// layer on" behaviour (docs/configuration/user.md).
func TestLoad_EmptyFile_StillLoads(t *testing.T) {
	path := writeTemp(t, "")
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: unexpected error on empty file: %v", err)
	}
}

// TestLoad_KnownKeysNeverRefused is a broad guard that every key the real
// schema defines, exercised together across every section Load's own
// tests already construct, continues to load. It complements the
// per-feature parsing tests elsewhere in this package by proving they
// still pass now that an unrecognized key is fatal: if validateKnownFields
// ever drifted from the struct tags Viper actually decodes against, this
// (and the rest of the suite) would start failing everywhere at once
// rather than only on the single key that drifted.
func TestLoad_KnownKeysNeverRefused(t *testing.T) {
	if _, err := Load(writeTemp(t, validYAML)); err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
}
