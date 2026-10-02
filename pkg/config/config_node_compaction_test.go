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

	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

func TestConfig_NodeCompaction_OffByDefaultAndParsed(t *testing.T) {
	if DefaultConfig().MVCC.NodeCompaction != nil {
		t.Fatal("the node keyspace has a compaction policy by default")
	}
	var m MVCCConfig
	if err := yaml.Unmarshal([]byte("node_compaction:\n  mode: revision\n  retain_revisions: 500\n"), &m); err != nil {
		t.Fatal(err)
	}
	if m.NodeCompaction == nil || m.NodeCompaction.Mode != compaction.Revision || m.NodeCompaction.RetainRevisions != 500 {
		t.Fatalf("NodeCompaction = %+v", m.NodeCompaction)
	}
}

func TestConfig_Validate_NodeCompaction(t *testing.T) {
	cfg := validConfig()
	cfg.MVCC.NodeCompaction = &compaction.Policy{Mode: compaction.Period, RetainPeriod: "24h"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid policy = %v", err)
	}
	cfg.MVCC.NodeCompaction = &compaction.Policy{Mode: compaction.Revision}
	var ve *ValidationError
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "mvcc.node_compaction" {
		t.Fatalf("invalid policy = %v", err)
	}
}
