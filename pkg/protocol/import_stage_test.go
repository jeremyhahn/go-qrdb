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

package protocol

import (
	"strings"
	"testing"
)

func TestImportStageRoundTrip(t *testing.T) {
	cmds := []ImportStageCommand{
		{
			Kind:        ImportStageSetMeta,
			SessionID:   "sess1",
			LeaseID:     7,
			Body:        []byte(`{"Tenant":"acme"}`),
			MaxSessions: 16,
			TenantID:    "acme",
		},
		{
			// MaxSessions == 0, TenantID empty: the unscoped, system-tenant
			// path, which still encodes and decodes cleanly.
			Kind:      ImportStageSetMeta,
			SessionID: "sess2",
			LeaseID:   1,
			Body:      []byte("{}"),
		},
		{
			Kind:      ImportStageSetChunk,
			SessionID: "sess1",
			LeaseID:   7,
			Seq:       3,
			Body:      []byte("chunk bytes"),
			MetaBody:  []byte(`{"Tenant":"acme","LastSeq":3}`),
		},
		{
			// A chunk with no MetaBody: the replay path, which updates
			// nothing about the session's metadata.
			Kind:      ImportStageSetChunk,
			SessionID: "sess1",
			LeaseID:   7,
			Seq:       0,
			Body:      []byte{}, // a zero-length chunk is valid
		},
	}
	for _, want := range cmds {
		encoded, err := EncodeImportStage(want)
		if err != nil {
			t.Fatalf("EncodeImportStage(%+v): %v", want, err)
		}
		if encoded[0] != OpImportStage {
			t.Fatalf("EncodeImportStage(%+v)[0] = %#x, want OpImportStage", want, encoded[0])
		}
		cmd, err := DecodeCommand(encoded)
		if err != nil {
			t.Fatalf("DecodeCommand: %v", err)
		}
		got, err := DecodeImportStage(cmd.Payload)
		if err != nil {
			t.Fatalf("DecodeImportStage: %v", err)
		}
		if got.Kind != want.Kind ||
			got.SessionID != want.SessionID ||
			got.LeaseID != want.LeaseID ||
			got.Seq != want.Seq ||
			string(got.Body) != string(want.Body) ||
			got.MaxSessions != want.MaxSessions ||
			got.TenantID != want.TenantID ||
			string(got.MetaBody) != string(want.MetaBody) {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
	}
}

func TestEncodeImportStage_Refuses(t *testing.T) {
	for name, cmd := range map[string]ImportStageCommand{
		"unknown kind":  {Kind: 99, SessionID: "sess1"},
		"empty session": {Kind: ImportStageSetMeta, SessionID: ""},
		"session too long": {
			Kind:      ImportStageSetMeta,
			SessionID: strings.Repeat("x", 70000),
		},
		"tenant too long": {
			Kind:      ImportStageSetMeta,
			SessionID: "sess1",
			TenantID:  strings.Repeat("x", 70000),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeImportStage(cmd); err == nil {
				t.Fatalf("EncodeImportStage accepted %+v", cmd)
			}
		})
	}
}

func TestDecodeImportStage_Refuses(t *testing.T) {
	valid, err := EncodeImportStage(ImportStageCommand{
		Kind: ImportStageSetMeta, SessionID: "sess1", LeaseID: 1, Body: []byte("x"), TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("EncodeImportStage: %v", err)
	}
	payload := valid[1:] // strip the op byte, matching dispatch's own split

	cases := map[string][]byte{
		"empty payload":              {},
		"payload too short":          payload[:2],
		"unknown kind":               append([]byte{0x7f}, payload[1:]...),
		"session id length overruns": append([]byte{payload[0], 0xff, 0xff}, payload[3:]...),
		"tenant id length overruns":  truncateAfterTenantLenField(t, payload),
		"meta body length overruns":  truncateAfterMetaLenField(t, payload),
	}
	for name, malformed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeImportStage(malformed); err == nil {
				t.Fatalf("DecodeImportStage(%q) did not refuse", malformed)
			}
		})
	}
}

// truncateAfterTenantLenField corrupts payload's tenant-id length field to
// claim more bytes than the payload actually carries, for
// TestDecodeImportStage_Refuses' "tenant id length overruns" case.
func truncateAfterTenantLenField(t *testing.T, payload []byte) []byte {
	t.Helper()
	// [kind:1][sessionIdLen:2][sessionId][leaseId:8][seq:8][maxSessions:4][tenantIdLen:2]...
	idLen := int(uint16(payload[1]) | uint16(payload[2])<<8)
	off := 3 + idLen + 8 + 8 + 4
	cp := append([]byte{}, payload[:off+2]...)
	cp[off] = 0xff
	cp[off+1] = 0xff
	return cp
}

// truncateAfterMetaLenField corrupts payload's meta-body length field to
// claim more bytes than the payload actually carries, for
// TestDecodeImportStage_Refuses' "meta body length overruns" case.
func truncateAfterMetaLenField(t *testing.T, payload []byte) []byte {
	t.Helper()
	idLen := int(uint16(payload[1]) | uint16(payload[2])<<8)
	off := 3 + idLen + 8 + 8 + 4
	tenantLen := int(uint16(payload[off]) | uint16(payload[off+1])<<8)
	off += 2 + tenantLen
	cp := append([]byte{}, payload[:off+4]...)
	cp[off] = 0xff
	cp[off+1] = 0xff
	cp[off+2] = 0xff
	cp[off+3] = 0xff
	return cp
}
