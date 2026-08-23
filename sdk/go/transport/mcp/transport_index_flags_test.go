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

package mcp

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestIndexCreate_FlagsSurviveTheRoundTrip covers go-qrdb#34 on the MCP plane.
// connectIndexInProcess runs the REAL pkg/api/mcp tool server, so this covers
// the client's parameter map, the tool schema and the handler's mapping into
// index.IndexConfig in one pass.
//
// The client sent only unique and sparse. Numeric was a transport.IndexConfig
// field no tool call ever carried, and case-insensitivity could not be
// expressed at all.
func TestIndexCreate_FlagsSurviveTheRoundTrip(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	err := tr.IndexCreate(context.Background(), "by_email", "email", transport.IndexConfig{
		Unique:          true,
		Sparse:          true,
		Numeric:         true,
		CaseInsensitive: true,
	})
	if err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	svc.mu.Lock()
	got := svc.lastCreateCfg
	svc.mu.Unlock()

	if !got.Unique || !got.Sparse {
		t.Errorf("unique/sparse lost: %+v", got)
	}
	if got.ValueType != index.IndexValueTypeNumeric {
		t.Errorf("ValueType = %v; want numeric — the flag never reached the service", got.ValueType)
	}
	if got.CaseSensitive {
		t.Error("a case-insensitive tool call produced CaseSensitive:true; the engine " +
			"would build a case-SENSITIVE index and answer a folded lookup empty " +
			"with a nil error")
	}
}

// TestIndexCreate_ZeroConfigIsTheDefaultIndex is the control and the polarity
// guard. index.IndexConfig names CaseSensitive for the DEFAULT, so its zero
// value folds case: a handler that omitted the field would turn every
// ordinary MCP-created index case-insensitive.
func TestIndexCreate_ZeroConfigIsTheDefaultIndex(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if err := tr.IndexCreate(context.Background(), "by_name", "name", transport.IndexConfig{}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	svc.mu.Lock()
	got := svc.lastCreateCfg
	svc.mu.Unlock()

	if !got.CaseSensitive {
		t.Error("a zero config produced a case-INSENSITIVE index; the default must " +
			"stay case-sensitive")
	}
	if got.Unique || got.Sparse || got.ValueType == index.IndexValueTypeNumeric {
		t.Errorf("a zero config produced %+v; want no claims", got)
	}
}
