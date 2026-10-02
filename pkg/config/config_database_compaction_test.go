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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"gopkg.in/yaml.v3"
)

func TestConfig_DatabaseCompaction_YAMLShape(t *testing.T) {
	var db DatabaseConfig
	doc := "name: events\nengine: kv\nbackend: pebble\ncompaction:\n  mode: period\n  retain_period: 720h\n"
	if err := yaml.Unmarshal([]byte(doc), &db); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if db.Compaction == nil || db.Compaction.Mode != compaction.Period || db.Compaction.RetainPeriod != "720h" {
		t.Fatalf("Compaction = %+v", db.Compaction)
	}

	doc = "name: events\nengine: kv\nbackend: pebble\ncompaction:\n  mode: revision\n  retain_revisions: 1000\n"
	db = DatabaseConfig{}
	if err := yaml.Unmarshal([]byte(doc), &db); err != nil || db.Compaction.RetainRevisions != 1000 {
		t.Fatalf("revision policy = %+v, %v", db.Compaction, err)
	}
}

func TestConfig_Validate_DatabaseCompaction(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{{Name: "events", Engine: "kv", Backend: "pebble",
		Compaction: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate(valid policy) = %v", err)
	}

	cfg.Databases[0].Compaction = &compaction.Policy{Mode: compaction.Period, RetainPeriod: "soon"}
	err := cfg.Validate()
	var dce *DatabaseConfigError
	if !errors.As(err, &dce) || !strings.Contains(dce.Message, "compaction") {
		t.Fatalf("Validate(invalid policy) = %v, want a DatabaseConfigError naming compaction", err)
	}
}
