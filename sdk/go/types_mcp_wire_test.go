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

package sdk_test

import (
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the MCP wire contract crosses the SDK boundary as aliases of
// pkg/api/mcp/wire, so an envelope built through the SDK is the one the
// server and the SDK's own MCP client read. A copy would fail these.
var (
	_ wire.Code                                             = sdk.MCPCodeNotFound
	_ *wire.ErrorEnvelope                                   = (*sdk.MCPErrorEnvelope)(nil)
	_ *wire.ErrorPayload                                    = (*sdk.MCPErrorPayload)(nil)
	_ func(string, sdk.MCPCode, string) *wire.ErrorEnvelope = sdk.NewMCPErrorEnvelope
	_ func(any) (*sdk.MCPErrorPayload, bool)                = sdk.DecodeMCPErrorPayload
)

// TestSDK_MCPCodes_AreTheWireCodes pins every re-exported code to the wire
// value and proves the SDK names all seventeen — a code added to the wire
// without an SDK name fails the count.
func TestSDK_MCPCodes_AreTheWireCodes(t *testing.T) {
	pairs := map[sdk.MCPCode]wire.Code{
		sdk.MCPCodeOK:                 wire.CodeOK,
		sdk.MCPCodeUnauthenticated:    wire.CodeUnauthenticated,
		sdk.MCPCodePermissionDenied:   wire.CodePermissionDenied,
		sdk.MCPCodeNotFound:           wire.CodeNotFound,
		sdk.MCPCodeUnavailable:        wire.CodeUnavailable,
		sdk.MCPCodeDeadlineExceeded:   wire.CodeDeadlineExceeded,
		sdk.MCPCodeResourceExhausted:  wire.CodeResourceExhausted,
		sdk.MCPCodeFailedPrecondition: wire.CodeFailedPrecondition,
		sdk.MCPCodeCanceled:           wire.CodeCanceled,
		sdk.MCPCodeInvalidArgument:    wire.CodeInvalidArgument,
		sdk.MCPCodeAlreadyExists:      wire.CodeAlreadyExists,
		sdk.MCPCodeUnimplemented:      wire.CodeUnimplemented,
		sdk.MCPCodeAborted:            wire.CodeAborted,
		sdk.MCPCodeOutOfRange:         wire.CodeOutOfRange,
		sdk.MCPCodeDataLoss:           wire.CodeDataLoss,
		sdk.MCPCodeUnknown:            wire.CodeUnknown,
		sdk.MCPCodeInternal:           wire.CodeInternal,
	}
	for sdkCode, wireCode := range pairs {
		if sdkCode != wireCode {
			t.Errorf("sdk %d != wire %d", sdkCode, wireCode)
		}
		if !sdkCode.Declared() {
			t.Errorf("%d is not a declared code", sdkCode)
		}
	}
	if got := len(sdk.MCPCodes()); got != len(pairs) {
		t.Fatalf("wire declares %d codes, the SDK names %d", got, len(pairs))
	}
}

// TestSDK_MCPErrorEnvelope_RoundTrips is the consumer behaviour: an envelope
// built with the SDK survives the JSON round trip an MCP client performs —
// StructuredContent arrives as a map, not the struct — and decodes back to
// the same classification. An absent envelope and an unknown code behave as
// the contract says.
func TestSDK_MCPErrorEnvelope_RoundTrips(t *testing.T) {
	env := sdk.NewMCPErrorEnvelope("xkms_get_cert", sdk.MCPCodeNotFound, "no certificate for serial")
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var structured any
	if err := json.Unmarshal(raw, &structured); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	payload, ok := sdk.DecodeMCPErrorPayload(structured)
	if !ok {
		t.Fatalf("envelope %s did not decode", raw)
	}
	if payload.Code != sdk.MCPCodeNotFound || payload.Status != "NOT_FOUND" ||
		payload.Tool != "xkms_get_cert" || payload.Message != "no certificate for serial" {
		t.Errorf("decoded %+v", payload)
	}

	if _, ok := sdk.DecodeMCPErrorPayload(map[string]any{"rows": 3}); ok {
		t.Error("a success payload decoded as an error envelope")
	}

	unknown, ok := sdk.DecodeMCPErrorPayload(map[string]any{"error": map[string]any{"code": -32050}})
	if !ok || unknown.Code.Declared() || unknown.Code.String() != "UNRECOGNIZED" {
		t.Errorf("an undeclared code must arrive as-is and undeclared, got %+v, %v", unknown, ok)
	}
}
