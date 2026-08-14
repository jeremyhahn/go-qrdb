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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
)

// nonUTF8CredentialValue is a credential payload whose leading bytes
// (0x00 0xff 0xfe 0x01) are invalid UTF-8. Credentials routinely carry such
// non-UTF-8 content (private keys, certificates, random tokens); these tests
// prove it round-trips byte-for-byte over MCP, which carries credential values
// as base64 (RFC 4648) tool arguments and results.
var nonUTF8CredentialValue = []byte{0x00, 0xff, 0xfe, 0x01, 'k', 'e', 'y'}

// TestHandleCredentialStoreRetrieve_BinaryRoundTrip proves qrdb_credential_store
// base64-decodes its value argument to raw bytes and qrdb_credential_retrieve
// base64-encodes the raw bytes, so a non-UTF-8 credential survives byte-for-byte.
func TestHandleCredentialStoreRetrieve_BinaryRoundTrip(t *testing.T) {
	var stored []byte
	svc := newMockCredentialService()
	svc.storeFn = func(_ context.Context, _ string, v []byte) error {
		stored = append([]byte(nil), v...)
		return nil
	}
	svc.retrieveFn = func(_ context.Context, _ string) ([]byte, error) {
		return stored, nil
	}

	storeReq := buildCallToolRequest(t, "qrdb_credential_store", map[string]any{
		"name":  "db",
		"value": base64.StdEncoding.EncodeToString(nonUTF8CredentialValue),
	})
	storeRes, err := handleCredentialStore(svc)(context.Background(), storeReq)
	if err != nil {
		t.Fatalf("handleCredentialStore: %v", err)
	}
	if storeRes.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, storeRes))
	}
	if string(stored) != string(nonUTF8CredentialValue) {
		t.Fatalf("service received %#v, want %#v", stored, nonUTF8CredentialValue)
	}

	retrieveReq := buildCallToolRequest(t, "qrdb_credential_retrieve", map[string]any{
		"name": "db",
	})
	retrieveRes, err := handleCredentialRetrieve(svc)(context.Background(), retrieveReq)
	if err != nil {
		t.Fatalf("handleCredentialRetrieve: %v", err)
	}
	if retrieveRes.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, retrieveRes))
	}
	got, decodeErr := base64.StdEncoding.DecodeString(resultText(t, retrieveRes))
	if decodeErr != nil {
		t.Fatalf("retrieve result is not valid base64: %v", decodeErr)
	}
	if string(got) != string(nonUTF8CredentialValue) {
		t.Errorf("round-tripped value = %#v, want %#v", got, nonUTF8CredentialValue)
	}
}

// TestHandleCredentialSubmit_BinaryRoundTrip proves qrdb_credential_submit
// base64-decodes its value argument to raw bytes before reaching the service.
func TestHandleCredentialSubmit_BinaryRoundTrip(t *testing.T) {
	var submitted []byte
	svc := newMockCredentialService()
	svc.submitFn = func(_ context.Context, _ string, v []byte) error {
		submitted = append([]byte(nil), v...)
		return nil
	}

	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{
		"name":  "db",
		"value": base64.StdEncoding.EncodeToString(nonUTF8CredentialValue),
	})
	res, err := handleCredentialSubmit(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialSubmit: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, res))
	}
	if string(submitted) != string(nonUTF8CredentialValue) {
		t.Errorf("service received %#v, want %#v", submitted, nonUTF8CredentialValue)
	}
}

// TestHandleCredentialRotate_BinaryRoundTrip proves qrdb_credential_rotate
// base64-decodes its new_value argument to raw bytes.
func TestHandleCredentialRotate_BinaryRoundTrip(t *testing.T) {
	var rotated []byte
	svc := newMockCredentialService()
	svc.rotateFn = func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
		rotated = append([]byte(nil), opts.NewValue...)
		return &credentials.RotateResult{Name: name, PreviousVersion: 1, NewVersion: 2}, nil
	}

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name":      "db",
		"new_value": base64.StdEncoding.EncodeToString(nonUTF8CredentialValue),
	})
	res, err := handleCredentialRotate(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialRotate: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, res))
	}
	if string(rotated) != string(nonUTF8CredentialValue) {
		t.Errorf("service received %#v, want %#v", rotated, nonUTF8CredentialValue)
	}
}

// TestHandleCredentialRetrieveVersion_BinaryRoundTrip proves the value field of
// the qrdb_credential_retrieve_version JSON result is base64-encoded so binary
// content survives byte-for-byte.
func TestHandleCredentialRetrieveVersion_BinaryRoundTrip(t *testing.T) {
	svc := newMockCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return nonUTF8CredentialValue, nil
	}

	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{
		"name":    "db",
		"version": 0,
	})
	res, err := handleCredentialRetrieveVersion(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialRetrieveVersion: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, res))
	}

	var out struct {
		Value []byte `json:"value"`
	}
	if jsonErr := json.Unmarshal([]byte(resultText(t, res)), &out); jsonErr != nil {
		t.Fatalf("unmarshal result: %v", jsonErr)
	}
	if string(out.Value) != string(nonUTF8CredentialValue) {
		t.Errorf("round-tripped value = %#v, want %#v", out.Value, nonUTF8CredentialValue)
	}
}

// TestHandleCredentialStore_InvalidBase64 proves a value argument that is not
// valid base64 yields a tool error rather than corrupting the credential.
func TestHandleCredentialStore_InvalidBase64(t *testing.T) {
	svc := newMockCredentialService()
	req := buildCallToolRequest(t, "qrdb_credential_store", map[string]any{
		"name":  "db",
		"value": "not valid base64!",
	})
	res, err := handleCredentialStore(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for invalid base64 value")
	}
}
