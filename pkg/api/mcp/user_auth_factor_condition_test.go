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
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// conditionFactorService is the real service over a store with the guarded
// writes a conditional add commits through.
func conditionFactorService(t *testing.T) service.UserAuthFactorServicer {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

// callAddTool runs the add tool with args over the base request and decodes
// the factor ID and version a success reports.
func callAddTool(t *testing.T, svc service.UserAuthFactorServicer, cred string, args map[string]any) (string, uint64, error) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte(cred)),
	}
	for k, v := range args {
		req.Params.Arguments.(map[string]any)[k] = v
	}
	res, err := handleUserAuthFactorAddTool(svc)(context.Background(), req)
	if err != nil {
		return "", 0, err
	}
	if res.IsError {
		t.Fatalf("add tool returned an error result: %+v", res.Content)
	}
	var out struct {
		Factor           map[string]any `json:"factor"`
		FactorSetVersion uint64         `json:"factor_set_version"`
	}
	if uerr := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &out); uerr != nil {
		t.Fatalf("decode add result: %v", uerr)
	}
	id, _ := out.Factor["id"].(string)
	return id, out.FactorSetVersion, nil
}

func TestMCP_AddConditional_envelopeCarriesReason(t *testing.T) {
	svc := conditionFactorService(t)

	id, version, err := callAddTool(t, svc, "cred-1", map[string]any{"if_no_active_type": true})
	if err != nil || version != 1 {
		t.Fatalf("conditional add = %d, %v; want version 1", version, err)
	}
	_, _, err = callAddTool(t, svc, "cred-2", map[string]any{"if_no_active_type": true})
	if !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v, want ErrFactorConditionFailed", err)
	}
	env, ok := classifiedToolError("qrdb_user_authfactor_add", err).StructuredContent.(*wire.ErrorEnvelope)
	if !ok || env.Error.Reason != auth.ReasonFactorConditionFailed {
		t.Fatalf("envelope = %+v, want reason %s", env, auth.ReasonFactorConditionFailed)
	}
	if got := auth.FactorConditionFailedFromMetadata(env.Error.Metadata); got.ExistingFactorID != id || got.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v, want it to name %s at version 1", got, id)
	}
}

func TestMCP_AddConditional_expectedVersionAndList(t *testing.T) {
	svc := conditionFactorService(t)

	if _, _, err := callAddTool(t, svc, "cred-1", map[string]any{"expected_factor_set_version": "0"}); err != nil {
		t.Fatalf("add at version 0: %v", err)
	}
	if _, _, err := callAddTool(t, svc, "cred-2", map[string]any{"expected_factor_set_version": "0"}); !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("stale add err = %v, want ErrFactorConditionFailed", err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"user_id": "42"}
	res, err := handleUserAuthFactorListTool(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list struct {
		Total            int    `json:"total"`
		FactorSetVersion uint64 `json:"factor_set_version"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &list); err != nil || list.Total != 1 || list.FactorSetVersion != 1 {
		t.Fatalf("list = %+v, %v; want 1 factor at version 1", list, err)
	}
}

func TestMCP_AddConditional_badExpectedVersion(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"user_id":                     "42",
		"type":                        "mtls",
		"credential_b64":              base64.StdEncoding.EncodeToString([]byte("cred")),
		"expected_factor_set_version": "not-a-number",
	}
	res, err := handleUserAuthFactorAddTool(conditionFactorService(t))(context.Background(), req)
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("add with a bad version = %+v, %v; want an error result", res, err)
	}
}
