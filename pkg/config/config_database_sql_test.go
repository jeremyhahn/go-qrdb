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

	"gopkg.in/yaml.v3"
)

func TestConfig_Validate_DatabaseSQLConcurrency(t *testing.T) {
	for _, mode := range []string{"", "optimistic", "pessimistic"} {
		cfg := validConfig()
		cfg.Databases = []DatabaseConfig{{Name: "shop", Backend: "pebble", SQL: &DatabaseSQLConfig{Concurrency: mode}}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", mode, err)
		}
	}

	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{{Name: "shop", Backend: "pebble", SQL: &DatabaseSQLConfig{Concurrency: "eager"}}}
	err := cfg.Validate()
	var dce *DatabaseConfigError
	if !errors.As(err, &dce) || !strings.Contains(dce.Message, "sql.concurrency") {
		t.Fatalf("Validate(eager) = %v, want a DatabaseConfigError naming sql.concurrency", err)
	}
}

func TestConfig_DatabaseSQL_YAMLShape(t *testing.T) {
	var db DatabaseConfig
	doc := "name: shop\nbackend: pebble\nsql:\n  concurrency: pessimistic\n"
	if err := yaml.Unmarshal([]byte(doc), &db); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if db.SQL == nil || db.SQL.Concurrency != "pessimistic" {
		t.Fatalf("SQL = %+v, want pessimistic", db.SQL)
	}
}
