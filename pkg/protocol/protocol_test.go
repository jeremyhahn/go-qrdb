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
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestInvalidCommandError_Error(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{
			name:   "simple reason",
			reason: "data too short",
			want:   "protocol: invalid command: data too short",
		},
		{
			name:   "empty reason",
			reason: "",
			want:   "protocol: invalid command: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidCommandError{Reason: tt.reason}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInvalidCommandError_As(t *testing.T) {
	err := &InvalidCommandError{Reason: "test"}
	var target *InvalidCommandError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *InvalidCommandError")
	}
	if target.Reason != "test" {
		t.Errorf("Reason = %q, want %q", target.Reason, "test")
	}
}

func TestUnknownOpError_Error(t *testing.T) {
	tests := []struct {
		name string
		op   byte
		want string
	}{
		{
			name: "zero op",
			op:   0,
			want: "protocol: unknown op code: 0",
		},
		{
			name: "max op",
			op:   255,
			want: "protocol: unknown op code: 255",
		},
		{
			name: "mid range op",
			op:   99,
			want: "protocol: unknown op code: " + strconv.Itoa(99),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &UnknownOpError{Op: tt.op}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnknownOpError_As(t *testing.T) {
	err := &UnknownOpError{Op: 42}
	var target *UnknownOpError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *UnknownOpError")
	}
	if target.Op != 42 {
		t.Errorf("Op = %d, want %d", target.Op, 42)
	}
}

// ---------------------------------------------------------------------------
// Constants verification
// ---------------------------------------------------------------------------

func TestOpCodeConstants(t *testing.T) {
	tests := []struct {
		name string
		op   byte
		want byte
	}{
		{"OpMVCCPut", OpMVCCPut, 10},
		{"OpMVCCDelete", OpMVCCDelete, 11},
		{"OpMVCCRange", OpMVCCRange, 12},
		{"OpMVCCCompact", OpMVCCCompact, 13},
		{"OpTransaction", OpTransaction, 20},
		{"OpBatch", OpBatch, 30},
		{"OpLeaseGrant", OpLeaseGrant, 40},
		{"OpLeaseRevoke", OpLeaseRevoke, 41},
		{"OpLeaseKeepAlive", OpLeaseKeepAlive, 42},
		{"OpIndexCreate", OpIndexCreate, 50},
		{"OpIndexDrop", OpIndexDrop, 51},
		{"OpTSWrite", OpTSWrite, 60},
		{"OpTSWriteBatch", OpTSWriteBatch, 61},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.op != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.op, tt.want)
			}
		})
	}
}

func TestFlagConstants(t *testing.T) {
	tests := []struct {
		name string
		flag byte
		want byte
	}{
		{"RangeFlagCountOnly", RangeFlagCountOnly, 1},
		{"RangeFlagKeysOnly", RangeFlagKeysOnly, 2},
		{"IndexFlagUnique", IndexFlagUnique, 1},
		{"IndexFlagSparse", IndexFlagSparse, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flag != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.flag, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DecodeCommand tests
// ---------------------------------------------------------------------------

func TestDecodeCommand(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantOp  byte
		wantLen int
		wantErr bool
	}{
		{
			name:    "valid command with payload",
			data:    []byte{OpMVCCPut, 0x01, 0x02, 0x03},
			wantOp:  OpMVCCPut,
			wantLen: 3,
		},
		{
			name:    "valid command no payload",
			data:    []byte{OpMVCCCompact},
			wantOp:  OpMVCCCompact,
			wantLen: 0,
		},
		{
			name:    "empty data",
			data:    []byte{},
			wantErr: true,
		},
		{
			name:    "nil data",
			data:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := DecodeCommand(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var ice *InvalidCommandError
				if !errors.As(err, &ice) {
					t.Errorf("expected *InvalidCommandError, got %T", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmd.Op != tt.wantOp {
				t.Errorf("Op = %d, want %d", cmd.Op, tt.wantOp)
			}
			if len(cmd.Payload) != tt.wantLen {
				t.Errorf("Payload len = %d, want %d", len(cmd.Payload), tt.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MVCC Put roundtrip tests
// ---------------------------------------------------------------------------

func TestMVCCPutRoundtrip(t *testing.T) {
	tests := []struct {
		name    string
		key     []byte
		value   []byte
		leaseID int64
	}{
		{
			name:    "standard key value",
			key:     []byte("mykey"),
			value:   []byte("myvalue"),
			leaseID: 12345,
		},
		{
			name:    "empty value",
			key:     []byte("key"),
			value:   []byte{},
			leaseID: 0,
		},
		{
			name:    "empty key",
			key:     []byte{},
			value:   []byte("val"),
			leaseID: -1,
		},
		{
			name:    "no lease",
			key:     []byte("k"),
			value:   []byte("v"),
			leaseID: 0,
		},
		{
			name:    "negative lease ID",
			key:     []byte("k"),
			value:   []byte("v"),
			leaseID: -999,
		},
		{
			name:    "max int64 lease",
			key:     []byte("k"),
			value:   []byte("v"),
			leaseID: math.MaxInt64,
		},
		{
			name:    "large key and value",
			key:     bytes.Repeat([]byte("K"), 1000),
			value:   bytes.Repeat([]byte("V"), 5000),
			leaseID: 42,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeMVCCPut(tt.key, tt.value, tt.leaseID)
			if encoded[0] != OpMVCCPut {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpMVCCPut)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}
			if cmd.Op != OpMVCCPut {
				t.Fatalf("Op = %d, want %d", cmd.Op, OpMVCCPut)
			}

			key, value, leaseID, err := DecodeMVCCPut(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeMVCCPut: %v", err)
			}
			if !bytes.Equal(key, tt.key) {
				t.Errorf("key = %q, want %q", key, tt.key)
			}
			if !bytes.Equal(value, tt.value) {
				t.Errorf("value = %q, want %q", value, tt.value)
			}
			if leaseID != tt.leaseID {
				t.Errorf("leaseID = %d, want %d", leaseID, tt.leaseID)
			}
		})
	}
}

func TestDecodeMVCCPut_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "key length exceeds payload",
			payload: []byte{0xFF, 0x00}, // keyLen=255 but only 0 bytes remain
			wantMsg: "key length exceeds payload",
		},
		{
			name:    "truncated lease ID",
			payload: append([]byte{0x01, 0x00}, []byte("k")...), // keyLen=1, key="k", missing leaseID
			wantMsg: "key length exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := DecodeMVCCPut(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MVCC Delete roundtrip tests
// ---------------------------------------------------------------------------

func TestMVCCDeleteRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{
			name: "standard key",
			key:  []byte("delete-me"),
		},
		{
			name: "empty key",
			key:  []byte{},
		},
		{
			name: "binary key",
			key:  []byte{0x00, 0xFF, 0x01, 0xFE},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeMVCCDelete(tt.key)
			if encoded[0] != OpMVCCDelete {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpMVCCDelete)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			key, err := DecodeMVCCDelete(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeMVCCDelete: %v", err)
			}
			if !bytes.Equal(key, tt.key) {
				t.Errorf("key = %q, want %q", key, tt.key)
			}
		})
	}
}

func TestDecodeMVCCDelete_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x05},
			wantMsg: "payload too short",
		},
		{
			name:    "key length exceeds payload",
			payload: []byte{0x0A, 0x00, 0x01}, // keyLen=10, only 1 byte of key
			wantMsg: "key length exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeMVCCDelete(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MVCC Range roundtrip tests
// ---------------------------------------------------------------------------

func TestMVCCRangeRoundtrip(t *testing.T) {
	tests := []struct {
		name      string
		start     []byte
		end       []byte
		revision  int64
		limit     int64
		countOnly bool
		keysOnly  bool
	}{
		{
			name:      "full range no flags",
			start:     []byte("a"),
			end:       []byte("z"),
			revision:  100,
			limit:     50,
			countOnly: false,
			keysOnly:  false,
		},
		{
			name:      "count only",
			start:     []byte("start"),
			end:       []byte("end"),
			revision:  1,
			limit:     0,
			countOnly: true,
			keysOnly:  false,
		},
		{
			name:      "keys only",
			start:     []byte("s"),
			end:       []byte("e"),
			revision:  0,
			limit:     100,
			countOnly: false,
			keysOnly:  true,
		},
		{
			name:      "both flags set",
			start:     []byte("a"),
			end:       []byte("b"),
			revision:  999,
			limit:     1,
			countOnly: true,
			keysOnly:  true,
		},
		{
			name:      "empty start and end",
			start:     []byte{},
			end:       []byte{},
			revision:  0,
			limit:     0,
			countOnly: false,
			keysOnly:  false,
		},
		{
			name:      "negative revision and limit",
			start:     []byte("x"),
			end:       []byte("y"),
			revision:  -1,
			limit:     -1,
			countOnly: false,
			keysOnly:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeMVCCRange(tt.start, tt.end, tt.revision, tt.limit, tt.countOnly, tt.keysOnly)
			if encoded[0] != OpMVCCRange {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpMVCCRange)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			params, err := DecodeMVCCRange(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeMVCCRange: %v", err)
			}
			if !bytes.Equal(params.Start, tt.start) {
				t.Errorf("Start = %q, want %q", params.Start, tt.start)
			}
			if !bytes.Equal(params.End, tt.end) {
				t.Errorf("End = %q, want %q", params.End, tt.end)
			}
			if params.Revision != tt.revision {
				t.Errorf("Revision = %d, want %d", params.Revision, tt.revision)
			}
			if params.Limit != tt.limit {
				t.Errorf("Limit = %d, want %d", params.Limit, tt.limit)
			}
			if params.CountOnly != tt.countOnly {
				t.Errorf("CountOnly = %v, want %v", params.CountOnly, tt.countOnly)
			}
			if params.KeysOnly != tt.keysOnly {
				t.Errorf("KeysOnly = %v, want %v", params.KeysOnly, tt.keysOnly)
			}
		})
	}
}

func TestDecodeMVCCRange_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x00},
			wantMsg: "payload too short",
		},
		{
			name:    "start length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "start length exceeds payload",
		},
		{
			name:    "missing end length",
			payload: []byte{0x01, 0x00, 'a'}, // start="a", no endLen
			wantMsg: "missing end length",
		},
		{
			name:    "end length exceeds payload",
			payload: []byte{0x00, 0x00, 0xFF, 0x00}, // startLen=0, endLen=255
			wantMsg: "end length exceeds payload",
		},
		{
			name: "missing revision/limit/flags",
			// startLen=0, endLen=0, then only 10 bytes (need 17)
			payload: append([]byte{0x00, 0x00, 0x00, 0x00}, make([]byte, 10)...),
			wantMsg: "missing revision/limit/flags",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeMVCCRange(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MVCC Compact roundtrip tests
// ---------------------------------------------------------------------------

func TestMVCCCompactRoundtrip(t *testing.T) {
	tests := []struct {
		name     string
		revision int64
	}{
		{name: "positive revision", revision: 42},
		{name: "zero revision", revision: 0},
		{name: "negative revision", revision: -100},
		{name: "max int64", revision: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeMVCCCompact(tt.revision)
			if encoded[0] != OpMVCCCompact {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpMVCCCompact)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			rev, err := DecodeMVCCCompact(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeMVCCCompact: %v", err)
			}
			if rev != tt.revision {
				t.Errorf("revision = %d, want %d", rev, tt.revision)
			}
		})
	}
}

func TestDecodeMVCCCompact_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "short payload",
			payload: []byte{0x01, 0x02, 0x03},
			wantMsg: "payload too short",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeMVCCCompact(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Lease Grant roundtrip tests
// ---------------------------------------------------------------------------

func TestLeaseGrantRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		ttl  int64
	}{
		{name: "standard ttl", ttl: 60},
		{name: "zero ttl", ttl: 0},
		{name: "negative ttl", ttl: -1},
		{name: "large ttl", ttl: 86400 * 365},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeLeaseGrant(tt.ttl)
			if encoded[0] != OpLeaseGrant {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpLeaseGrant)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			ttl, err := DecodeLeaseGrant(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeLeaseGrant: %v", err)
			}
			if ttl != tt.ttl {
				t.Errorf("ttl = %d, want %d", ttl, tt.ttl)
			}
		})
	}
}

func TestDecodeLeaseGrant_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "seven bytes",
			payload: make([]byte, 7),
			wantMsg: "payload too short",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeLeaseGrant(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Lease Revoke roundtrip tests
// ---------------------------------------------------------------------------

func TestLeaseRevokeRoundtrip(t *testing.T) {
	tests := []struct {
		name    string
		leaseID int64
	}{
		{name: "positive lease", leaseID: 12345},
		{name: "zero lease", leaseID: 0},
		{name: "negative lease", leaseID: -999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeLeaseRevoke(tt.leaseID)
			if encoded[0] != OpLeaseRevoke {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpLeaseRevoke)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			leaseID, err := DecodeLeaseRevoke(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeLeaseRevoke: %v", err)
			}
			if leaseID != tt.leaseID {
				t.Errorf("leaseID = %d, want %d", leaseID, tt.leaseID)
			}
		})
	}
}

func TestDecodeLeaseRevoke_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "too short",
			payload: make([]byte, 5),
			wantMsg: "payload too short",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeLeaseRevoke(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Lease KeepAlive roundtrip tests
// ---------------------------------------------------------------------------

func TestLeaseKeepAliveRoundtrip(t *testing.T) {
	tests := []struct {
		name    string
		leaseID int64
	}{
		{name: "positive lease", leaseID: 77777},
		{name: "zero lease", leaseID: 0},
		{name: "min int64", leaseID: math.MinInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeLeaseKeepAlive(tt.leaseID)
			if encoded[0] != OpLeaseKeepAlive {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpLeaseKeepAlive)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			leaseID, err := DecodeLeaseKeepAlive(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeLeaseKeepAlive: %v", err)
			}
			if leaseID != tt.leaseID {
				t.Errorf("leaseID = %d, want %d", leaseID, tt.leaseID)
			}
		})
	}
}

func TestDecodeLeaseKeepAlive_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "six bytes",
			payload: make([]byte, 6),
			wantMsg: "payload too short",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeLeaseKeepAlive(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Index Create roundtrip tests
// ---------------------------------------------------------------------------

func TestIndexCreateRoundtrip(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		fieldName string
		unique    bool
		sparse    bool
		numeric   bool
	}{
		{
			name:      "unique index",
			indexName: "idx_email",
			fieldName: "email",
			unique:    true,
			sparse:    false,
			numeric:   false,
		},
		{
			name:      "sparse index",
			indexName: "idx_opt",
			fieldName: "optional_field",
			unique:    false,
			sparse:    true,
			numeric:   false,
		},
		{
			name:      "both flags",
			indexName: "idx_both",
			fieldName: "field",
			unique:    true,
			sparse:    true,
			numeric:   false,
		},
		{
			name:      "no flags",
			indexName: "idx_plain",
			fieldName: "name",
			unique:    false,
			sparse:    false,
			numeric:   false,
		},
		{
			name:      "empty name and field",
			indexName: "",
			fieldName: "",
			unique:    false,
			sparse:    false,
			numeric:   false,
		},
		{
			name:      "numeric index",
			indexName: "idx_score",
			fieldName: "score",
			unique:    false,
			sparse:    false,
			numeric:   true,
		},
		{
			name:      "unique numeric index",
			indexName: "idx_rank",
			fieldName: "rank",
			unique:    true,
			sparse:    false,
			numeric:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeIndexCreate(tt.indexName, tt.fieldName, tt.unique, tt.sparse, tt.numeric)
			if encoded[0] != OpIndexCreate {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpIndexCreate)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			name, fieldName, unique, sparse, numeric, err := DecodeIndexCreate(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeIndexCreate: %v", err)
			}
			if name != tt.indexName {
				t.Errorf("name = %q, want %q", name, tt.indexName)
			}
			if fieldName != tt.fieldName {
				t.Errorf("fieldName = %q, want %q", fieldName, tt.fieldName)
			}
			if unique != tt.unique {
				t.Errorf("unique = %v, want %v", unique, tt.unique)
			}
			if sparse != tt.sparse {
				t.Errorf("sparse = %v, want %v", sparse, tt.sparse)
			}
			if numeric != tt.numeric {
				t.Errorf("numeric = %v, want %v", numeric, tt.numeric)
			}
		})
	}
}

func TestDecodeIndexCreate_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "name length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "name length exceeds payload",
		},
		{
			name:    "missing field length",
			payload: []byte{0x01, 0x00, 'a'}, // name="a", no field length
			wantMsg: "missing field length",
		},
		{
			name:    "field length exceeds payload",
			payload: []byte{0x00, 0x00, 0xFF, 0x00}, // nameLen=0, fieldLen=255
			wantMsg: "field length exceeds payload",
		},
		{
			name:    "missing flags",
			payload: []byte{0x00, 0x00, 0x00, 0x00}, // nameLen=0, fieldLen=0, no flags byte
			wantMsg: "missing flags",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, _, err := DecodeIndexCreate(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Index Drop roundtrip tests
// ---------------------------------------------------------------------------

func TestIndexDropRoundtrip(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
	}{
		{name: "standard name", indexName: "idx_email"},
		{name: "empty name", indexName: ""},
		{name: "long name", indexName: strings.Repeat("x", 500)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeIndexDrop(tt.indexName)
			if encoded[0] != OpIndexDrop {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpIndexDrop)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			name, err := DecodeIndexDrop(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeIndexDrop: %v", err)
			}
			if name != tt.indexName {
				t.Errorf("name = %q, want %q", name, tt.indexName)
			}
		})
	}
}

func TestDecodeIndexDrop_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "name length exceeds payload",
			payload: []byte{0x0A, 0x00, 0x01}, // nameLen=10, only 1 byte
			wantMsg: "name length exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeIndexDrop(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TSWrite roundtrip tests
// ---------------------------------------------------------------------------

func TestTSWriteRoundtrip(t *testing.T) {
	tests := []struct {
		name      string
		metric    string
		timestamp int64
		value     float64
		tags      map[string]string
	}{
		{
			name:      "with tags",
			metric:    "cpu.usage",
			timestamp: 1700000000,
			value:     98.6,
			tags:      map[string]string{"host": "node-1", "region": "us-east"},
		},
		{
			name:      "no tags",
			metric:    "mem.free",
			timestamp: 1700000001,
			value:     1024.0,
			tags:      map[string]string{},
		},
		{
			name:      "nil tags encoded as empty",
			metric:    "disk.io",
			timestamp: 0,
			value:     0.0,
			tags:      nil,
		},
		{
			name:      "single tag",
			metric:    "net.rx",
			timestamp: -1,
			value:     -1.5,
			tags:      map[string]string{"iface": "eth0"},
		},
		{
			name:      "NaN value",
			metric:    "error.rate",
			timestamp: 100,
			value:     math.NaN(),
			tags:      map[string]string{},
		},
		{
			name:      "positive infinity",
			metric:    "inf.metric",
			timestamp: 200,
			value:     math.Inf(1),
			tags:      map[string]string{},
		},
		{
			name:      "negative infinity",
			metric:    "neginf.metric",
			timestamp: 300,
			value:     math.Inf(-1),
			tags:      map[string]string{},
		},
		{
			name:      "empty metric name",
			metric:    "",
			timestamp: 500,
			value:     1.0,
			tags:      map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeTSWrite(tt.metric, tt.timestamp, tt.value, tt.tags)
			if encoded[0] != OpTSWrite {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpTSWrite)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			params, err := DecodeTSWrite(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeTSWrite: %v", err)
			}
			if params.Metric != tt.metric {
				t.Errorf("Metric = %q, want %q", params.Metric, tt.metric)
			}
			if params.Timestamp != tt.timestamp {
				t.Errorf("Timestamp = %d, want %d", params.Timestamp, tt.timestamp)
			}

			// NaN != NaN, so use math.IsNaN for comparison.
			if math.IsNaN(tt.value) {
				if !math.IsNaN(params.Value) {
					t.Errorf("Value = %v, want NaN", params.Value)
				}
			} else if params.Value != tt.value {
				t.Errorf("Value = %v, want %v", params.Value, tt.value)
			}

			expectedTags := tt.tags
			if expectedTags == nil {
				expectedTags = map[string]string{}
			}
			if len(params.Tags) != len(expectedTags) {
				t.Fatalf("Tags count = %d, want %d", len(params.Tags), len(expectedTags))
			}
			for k, v := range expectedTags {
				if got, ok := params.Tags[k]; !ok {
					t.Errorf("missing tag key %q", k)
				} else if got != v {
					t.Errorf("tag[%q] = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestDecodeTSWrite_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "metric length exceeds payload",
			payload: []byte{0xFF, 0x00},
			wantMsg: "metric length exceeds payload",
		},
		{
			name:    "missing timestamp/value/tagCount",
			payload: []byte{0x01, 0x00, 'm'}, // metric="m", then need 18 more bytes
			wantMsg: "missing timestamp/value/tagCount",
		},
		{
			name: "truncated tag key length",
			// metricLen=0, timestamp(8), value(8), tagCount=1, then nothing
			payload: func() []byte {
				b := make([]byte, 2+0+8+8+2)
				// metricLen = 0
				b[0] = 0x00
				b[1] = 0x00
				// tagCount = 1
				b[18] = 0x01
				b[19] = 0x00
				return b
			}(),
			wantMsg: "truncated tag key length",
		},
		{
			name: "tag key exceeds payload",
			payload: func() []byte {
				b := make([]byte, 2+0+8+8+2+2)
				// tagCount = 1
				b[18] = 0x01
				b[19] = 0x00
				// tag key length = 255
				b[20] = 0xFF
				b[21] = 0x00
				return b
			}(),
			wantMsg: "tag key exceeds payload",
		},
		{
			name: "truncated tag value length",
			payload: func() []byte {
				b := make([]byte, 2+0+8+8+2+2+1) // metric(2), ts(8), val(8), tagCount(2), keyLen(2), key(1)
				b[18] = 0x01                     // tagCount = 1
				b[19] = 0x00
				b[20] = 0x01 // keyLen = 1
				b[21] = 0x00
				b[22] = 'k' // key = "k"
				return b
			}(),
			wantMsg: "truncated tag value length",
		},
		{
			name: "tag value exceeds payload",
			payload: func() []byte {
				b := make([]byte, 2+0+8+8+2+2+1+2) // +valLen
				b[18] = 0x01                       // tagCount = 1
				b[19] = 0x00
				b[20] = 0x01 // keyLen = 1
				b[21] = 0x00
				b[22] = 'k'  // key = "k"
				b[23] = 0xFF // valLen = 255
				b[24] = 0x00
				return b
			}(),
			wantMsg: "tag value exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeTSWrite(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Op roundtrip tests
// ---------------------------------------------------------------------------

func TestOpRoundtrip(t *testing.T) {
	tests := []struct {
		name     string
		opType   byte
		key      []byte
		value    []byte
		rangeEnd []byte
		leaseID  int64
	}{
		{
			name:     "full op",
			opType:   OpMVCCPut,
			key:      []byte("mykey"),
			value:    []byte("myvalue"),
			rangeEnd: []byte("myend"),
			leaseID:  42,
		},
		{
			name:     "empty fields",
			opType:   OpMVCCDelete,
			key:      []byte{},
			value:    []byte{},
			rangeEnd: []byte{},
			leaseID:  0,
		},
		{
			name:     "key only",
			opType:   OpMVCCRange,
			key:      []byte("start"),
			value:    []byte{},
			rangeEnd: []byte{},
			leaseID:  -1,
		},
		{
			name:     "negative lease",
			opType:   OpMVCCPut,
			key:      []byte("k"),
			value:    []byte("v"),
			rangeEnd: []byte{},
			leaseID:  math.MinInt64,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeOp(tt.opType, tt.key, tt.value, tt.rangeEnd, tt.leaseID)

			op, n, err := DecodeOp(encoded)
			if err != nil {
				t.Fatalf("DecodeOp: %v", err)
			}
			if n != len(encoded) {
				t.Errorf("consumed = %d, want %d", n, len(encoded))
			}
			if op.Type != tt.opType {
				t.Errorf("Type = %d, want %d", op.Type, tt.opType)
			}
			if !bytes.Equal(op.Key, tt.key) {
				t.Errorf("Key = %q, want %q", op.Key, tt.key)
			}
			if !bytes.Equal(op.Value, tt.value) {
				t.Errorf("Value = %q, want %q", op.Value, tt.value)
			}
			if !bytes.Equal(op.RangeEnd, tt.rangeEnd) {
				t.Errorf("RangeEnd = %q, want %q", op.RangeEnd, tt.rangeEnd)
			}
			if op.LeaseID != tt.leaseID {
				t.Errorf("LeaseID = %d, want %d", op.LeaseID, tt.leaseID)
			}
		})
	}
}

func TestDecodeOp_Errors(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantMsg string
	}{
		{
			name:    "empty data",
			data:    []byte{},
			wantMsg: "data too short",
		},
		{
			name:    "missing key length",
			data:    []byte{OpMVCCPut},
			wantMsg: "missing key length",
		},
		{
			name:    "key exceeds data",
			data:    []byte{OpMVCCPut, 0xFF, 0x00},
			wantMsg: "key exceeds data",
		},
		{
			name:    "missing value length",
			data:    []byte{OpMVCCPut, 0x00, 0x00}, // keyLen=0, no valLen
			wantMsg: "missing value length",
		},
		{
			name:    "value exceeds data",
			data:    []byte{OpMVCCPut, 0x00, 0x00, 0xFF, 0x00}, // keyLen=0, valLen=255
			wantMsg: "value exceeds data",
		},
		{
			name:    "missing range end length",
			data:    []byte{OpMVCCPut, 0x00, 0x00, 0x00, 0x00}, // keyLen=0, valLen=0, no reLen
			wantMsg: "missing range end length",
		},
		{
			name:    "range end exceeds data",
			data:    []byte{OpMVCCPut, 0x00, 0x00, 0x00, 0x00, 0xFF, 0x00},
			wantMsg: "range end exceeds data",
		},
		{
			name:    "missing lease ID",
			data:    []byte{OpMVCCPut, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			wantMsg: "missing lease ID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeOp(tt.data)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Compare roundtrip tests
// ---------------------------------------------------------------------------

func TestCompareRoundtrip(t *testing.T) {
	tests := []struct {
		name           string
		key            []byte
		target         byte
		result         byte
		value          []byte
		version        int64
		createRevision int64
		modRevision    int64
	}{
		{
			name:           "full compare",
			key:            []byte("compare-key"),
			target:         1,
			result:         2,
			value:          []byte("expected-val"),
			version:        10,
			createRevision: 5,
			modRevision:    8,
		},
		{
			name:           "empty key and value",
			key:            []byte{},
			target:         0,
			result:         0,
			value:          []byte{},
			version:        0,
			createRevision: 0,
			modRevision:    0,
		},
		{
			name:           "negative revisions",
			key:            []byte("k"),
			target:         255,
			result:         255,
			value:          []byte("v"),
			version:        -1,
			createRevision: -100,
			modRevision:    -200,
		},
		{
			name:           "max revisions",
			key:            []byte("k"),
			target:         3,
			result:         4,
			value:          []byte{},
			version:        math.MaxInt64,
			createRevision: math.MaxInt64,
			modRevision:    math.MaxInt64,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeCompare(tt.key, tt.target, tt.result, tt.value, tt.version, tt.createRevision, tt.modRevision)

			cmp, n, err := DecodeCompare(encoded)
			if err != nil {
				t.Fatalf("DecodeCompare: %v", err)
			}
			if n != len(encoded) {
				t.Errorf("consumed = %d, want %d", n, len(encoded))
			}
			if !bytes.Equal(cmp.Key, tt.key) {
				t.Errorf("Key = %q, want %q", cmp.Key, tt.key)
			}
			if cmp.Target != tt.target {
				t.Errorf("Target = %d, want %d", cmp.Target, tt.target)
			}
			if cmp.Result != tt.result {
				t.Errorf("Result = %d, want %d", cmp.Result, tt.result)
			}
			if !bytes.Equal(cmp.Value, tt.value) {
				t.Errorf("Value = %q, want %q", cmp.Value, tt.value)
			}
			if cmp.Version != tt.version {
				t.Errorf("Version = %d, want %d", cmp.Version, tt.version)
			}
			if cmp.CreateRevision != tt.createRevision {
				t.Errorf("CreateRevision = %d, want %d", cmp.CreateRevision, tt.createRevision)
			}
			if cmp.ModRevision != tt.modRevision {
				t.Errorf("ModRevision = %d, want %d", cmp.ModRevision, tt.modRevision)
			}
		})
	}
}

func TestDecodeCompare_Errors(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantMsg string
	}{
		{
			name:    "empty data",
			data:    []byte{},
			wantMsg: "data too short",
		},
		{
			name:    "single byte",
			data:    []byte{0x01},
			wantMsg: "data too short",
		},
		{
			name:    "key exceeds data",
			data:    []byte{0xFF, 0x00},
			wantMsg: "key exceeds data",
		},
		{
			name:    "missing target/result",
			data:    []byte{0x00, 0x00}, // keyLen=0, no target/result
			wantMsg: "missing target/result",
		},
		{
			name:    "missing value length",
			data:    []byte{0x00, 0x00, 0x01, 0x02}, // keyLen=0, target=1, result=2, no valLen
			wantMsg: "missing value length",
		},
		{
			name:    "value exceeds data",
			data:    []byte{0x00, 0x00, 0x01, 0x02, 0xFF, 0x00}, // valLen=255
			wantMsg: "value exceeds data",
		},
		{
			name:    "missing version/revisions",
			data:    []byte{0x00, 0x00, 0x01, 0x02, 0x00, 0x00}, // keyLen=0, t, r, valLen=0, need 24 more
			wantMsg: "missing version/revisions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeCompare(tt.data)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transaction roundtrip tests
// ---------------------------------------------------------------------------

func TestTransactionRoundtrip(t *testing.T) {
	tests := []struct {
		name       string
		compares   [][]byte
		successOps [][]byte
		failureOps [][]byte
	}{
		{
			name: "full transaction",
			compares: [][]byte{
				EncodeCompare([]byte("key1"), 1, 0, []byte("val1"), 10, 5, 8),
			},
			successOps: [][]byte{
				EncodeOp(OpMVCCPut, []byte("key1"), []byte("new-val"), nil, 0),
			},
			failureOps: [][]byte{
				EncodeOp(OpMVCCDelete, []byte("key1"), nil, nil, 0),
			},
		},
		{
			name:       "empty transaction",
			compares:   nil,
			successOps: nil,
			failureOps: nil,
		},
		{
			name:       "zero compares zero ops",
			compares:   [][]byte{},
			successOps: [][]byte{},
			failureOps: [][]byte{},
		},
		{
			name: "multiple compares and ops",
			compares: [][]byte{
				EncodeCompare([]byte("k1"), 1, 0, []byte("v1"), 1, 1, 1),
				EncodeCompare([]byte("k2"), 2, 1, []byte("v2"), 2, 2, 2),
			},
			successOps: [][]byte{
				EncodeOp(OpMVCCPut, []byte("k1"), []byte("new1"), nil, 100),
				EncodeOp(OpMVCCPut, []byte("k2"), []byte("new2"), nil, 200),
			},
			failureOps: [][]byte{
				EncodeOp(OpMVCCDelete, []byte("k1"), nil, nil, 0),
			},
		},
		{
			name: "success only",
			compares: [][]byte{
				EncodeCompare([]byte("k"), 0, 0, nil, 0, 0, 0),
			},
			successOps: [][]byte{
				EncodeOp(OpMVCCPut, []byte("k"), []byte("v"), nil, 0),
			},
			failureOps: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeTransaction(tt.compares, tt.successOps, tt.failureOps)
			if encoded[0] != OpTransaction {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpTransaction)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			txn, err := DecodeTransaction(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeTransaction: %v", err)
			}

			expectedCompares := tt.compares
			if expectedCompares == nil {
				expectedCompares = [][]byte{}
			}
			if len(txn.Compares) != len(expectedCompares) {
				t.Fatalf("Compares count = %d, want %d", len(txn.Compares), len(expectedCompares))
			}

			expectedSuccess := tt.successOps
			if expectedSuccess == nil {
				expectedSuccess = [][]byte{}
			}
			if len(txn.SuccessOps) != len(expectedSuccess) {
				t.Fatalf("SuccessOps count = %d, want %d", len(txn.SuccessOps), len(expectedSuccess))
			}

			expectedFailure := tt.failureOps
			if expectedFailure == nil {
				expectedFailure = [][]byte{}
			}
			if len(txn.FailureOps) != len(expectedFailure) {
				t.Fatalf("FailureOps count = %d, want %d", len(txn.FailureOps), len(expectedFailure))
			}

			// Verify first compare's key if present.
			if len(tt.compares) > 0 {
				origCmp, _, err := DecodeCompare(tt.compares[0])
				if err != nil {
					t.Fatalf("DecodeCompare original: %v", err)
				}
				if !bytes.Equal(txn.Compares[0].Key, origCmp.Key) {
					t.Errorf("Compare[0].Key = %q, want %q", txn.Compares[0].Key, origCmp.Key)
				}
			}

			// Verify first success op's key if present.
			if len(tt.successOps) > 0 {
				origOp, _, err := DecodeOp(tt.successOps[0])
				if err != nil {
					t.Fatalf("DecodeOp original success: %v", err)
				}
				if !bytes.Equal(txn.SuccessOps[0].Key, origOp.Key) {
					t.Errorf("SuccessOps[0].Key = %q, want %q", txn.SuccessOps[0].Key, origOp.Key)
				}
			}

			// Verify first failure op's key if present.
			if len(tt.failureOps) > 0 {
				origOp, _, err := DecodeOp(tt.failureOps[0])
				if err != nil {
					t.Fatalf("DecodeOp original failure: %v", err)
				}
				if !bytes.Equal(txn.FailureOps[0].Key, origOp.Key) {
					t.Errorf("FailureOps[0].Key = %q, want %q", txn.FailureOps[0].Key, origOp.Key)
				}
			}
		})
	}
}

func TestDecodeTransaction_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "missing compare count",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "missing compare count",
		},
		{
			name:    "truncated compare length",
			payload: []byte{0x01, 0x00}, // compareCount=1 but no compare data
			wantMsg: "truncated compare length",
		},
		{
			name:    "compare exceeds payload",
			payload: []byte{0x01, 0x00, 0xFF, 0x00}, // compareCount=1, cLen=255
			wantMsg: "compare exceeds payload",
		},
		{
			name: "missing success count",
			// compareCount=0, then no more data for success count
			payload: []byte{0x00, 0x00},
			wantMsg: "missing success count",
		},
		{
			name: "truncated success op length",
			// compareCount=0, successCount=1, then nothing
			payload: []byte{0x00, 0x00, 0x01, 0x00},
			wantMsg: "truncated success op length",
		},
		{
			name: "success op exceeds payload",
			// compareCount=0, successCount=1, opLen=255
			payload: []byte{0x00, 0x00, 0x01, 0x00, 0xFF, 0x00},
			wantMsg: "success op exceeds payload",
		},
		{
			name: "missing failure count",
			// compareCount=0, successCount=0, then nothing for failure count
			payload: []byte{0x00, 0x00, 0x00, 0x00},
			wantMsg: "missing failure count",
		},
		{
			name: "truncated failure op length",
			// compareCount=0, successCount=0, failureCount=1, then nothing
			payload: []byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x00},
			wantMsg: "truncated failure op length",
		},
		{
			name: "failure op exceeds payload",
			// compareCount=0, successCount=0, failureCount=1, opLen=255
			payload: []byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0xFF, 0x00},
			wantMsg: "failure op exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeTransaction(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// TestDecodeTransaction_NestedCompareError verifies that a malformed compare
// embedded inside a transaction payload propagates the inner decode error.
func TestDecodeTransaction_NestedCompareError(t *testing.T) {
	// Build a transaction payload with compareCount=1, cLen=1, and a single
	// byte that is too short for DecodeCompare.
	payload := []byte{
		0x01, 0x00, // compareCount = 1
		0x01, 0x00, // cLen = 1
		0xFF, // single invalid compare byte
	}
	_, err := DecodeTransaction(payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ice *InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T", err)
	}
}

// TestDecodeTransaction_NestedOpError verifies that a malformed op
// embedded inside a transaction success/failure list propagates the error.
func TestDecodeTransaction_NestedOpError(t *testing.T) {
	// Build a transaction payload: 0 compares, 1 success op of length 1 (invalid).
	payload := []byte{
		0x00, 0x00, // compareCount = 0
		0x01, 0x00, // successCount = 1
		0x01, 0x00, // opLen = 1
		0xFF, // single invalid op byte (too short for DecodeOp)
	}
	_, err := DecodeTransaction(payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ice *InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Batch roundtrip tests
// ---------------------------------------------------------------------------

func TestBatchRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		ops  [][]byte
	}{
		{
			name: "single op",
			ops: [][]byte{
				EncodeOp(OpMVCCPut, []byte("k1"), []byte("v1"), nil, 100),
			},
		},
		{
			name: "multiple ops",
			ops: [][]byte{
				EncodeOp(OpMVCCPut, []byte("k1"), []byte("v1"), nil, 0),
				EncodeOp(OpMVCCDelete, []byte("k2"), nil, nil, 0),
				EncodeOp(OpMVCCPut, []byte("k3"), []byte("v3"), []byte("end3"), 42),
			},
		},
		{
			name: "empty batch",
			ops:  [][]byte{},
		},
		{
			name: "nil batch",
			ops:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeBatch(tt.ops)
			if encoded[0] != OpBatch {
				t.Fatalf("op byte = %d, want %d", encoded[0], OpBatch)
			}

			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			ops, err := DecodeBatch(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeBatch: %v", err)
			}

			expectedLen := len(tt.ops)
			if tt.ops == nil {
				expectedLen = 0
			}
			if len(ops) != expectedLen {
				t.Fatalf("ops count = %d, want %d", len(ops), expectedLen)
			}

			// Verify each op matches original encoding.
			for i, rawOp := range tt.ops {
				orig, _, err := DecodeOp(rawOp)
				if err != nil {
					t.Fatalf("DecodeOp original[%d]: %v", i, err)
				}
				if ops[i].Type != orig.Type {
					t.Errorf("ops[%d].Type = %d, want %d", i, ops[i].Type, orig.Type)
				}
				if !bytes.Equal(ops[i].Key, orig.Key) {
					t.Errorf("ops[%d].Key = %q, want %q", i, ops[i].Key, orig.Key)
				}
				if !bytes.Equal(ops[i].Value, orig.Value) {
					t.Errorf("ops[%d].Value = %q, want %q", i, ops[i].Value, orig.Value)
				}
				if !bytes.Equal(ops[i].RangeEnd, orig.RangeEnd) {
					t.Errorf("ops[%d].RangeEnd = %q, want %q", i, ops[i].RangeEnd, orig.RangeEnd)
				}
				if ops[i].LeaseID != orig.LeaseID {
					t.Errorf("ops[%d].LeaseID = %d, want %d", i, ops[i].LeaseID, orig.LeaseID)
				}
			}
		})
	}
}

func TestDecodeBatch_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{
			name:    "empty payload",
			payload: []byte{},
			wantMsg: "payload too short",
		},
		{
			name:    "single byte",
			payload: []byte{0x01},
			wantMsg: "payload too short",
		},
		{
			name:    "truncated op length",
			payload: []byte{0x01, 0x00}, // opCount=1, no op data
			wantMsg: "truncated op length",
		},
		{
			name:    "op exceeds payload",
			payload: []byte{0x01, 0x00, 0xFF, 0x00}, // opCount=1, opLen=255
			wantMsg: "op exceeds payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeBatch(tt.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("expected *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// TestDecodeBatch_NestedOpError verifies that a malformed op inside a batch
// propagates the inner decode error.
func TestDecodeBatch_NestedOpError(t *testing.T) {
	// opCount=1, opLen=1, single invalid byte.
	payload := []byte{
		0x01, 0x00, // opCount = 1
		0x01, 0x00, // opLen = 1
		0xFF, // invalid op (too short)
	}
	_, err := DecodeBatch(payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ice *InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Op code in encoded output verification
// ---------------------------------------------------------------------------

func TestEncodedOpCodes(t *testing.T) {
	tests := []struct {
		name   string
		encode func() []byte
		wantOp byte
	}{
		{
			name:   "EncodeMVCCPut",
			encode: func() []byte { return EncodeMVCCPut([]byte("k"), []byte("v"), 0) },
			wantOp: OpMVCCPut,
		},
		{
			name:   "EncodeMVCCDelete",
			encode: func() []byte { return EncodeMVCCDelete([]byte("k")) },
			wantOp: OpMVCCDelete,
		},
		{
			name:   "EncodeMVCCRange",
			encode: func() []byte { return EncodeMVCCRange([]byte("a"), []byte("z"), 0, 0, false, false) },
			wantOp: OpMVCCRange,
		},
		{
			name:   "EncodeMVCCCompact",
			encode: func() []byte { return EncodeMVCCCompact(1) },
			wantOp: OpMVCCCompact,
		},
		{
			name:   "EncodeLeaseGrant",
			encode: func() []byte { return EncodeLeaseGrant(60) },
			wantOp: OpLeaseGrant,
		},
		{
			name:   "EncodeLeaseRevoke",
			encode: func() []byte { return EncodeLeaseRevoke(1) },
			wantOp: OpLeaseRevoke,
		},
		{
			name:   "EncodeLeaseKeepAlive",
			encode: func() []byte { return EncodeLeaseKeepAlive(1) },
			wantOp: OpLeaseKeepAlive,
		},
		{
			name:   "EncodeIndexCreate",
			encode: func() []byte { return EncodeIndexCreate("idx", "field", true, false, false) },
			wantOp: OpIndexCreate,
		},
		{
			name:   "EncodeIndexDrop",
			encode: func() []byte { return EncodeIndexDrop("idx") },
			wantOp: OpIndexDrop,
		},
		{
			name: "EncodeTSWrite",
			encode: func() []byte {
				return EncodeTSWrite("metric", 0, 0, nil)
			},
			wantOp: OpTSWrite,
		},
		{
			name: "EncodeTransaction",
			encode: func() []byte {
				return EncodeTransaction(nil, nil, nil)
			},
			wantOp: OpTransaction,
		},
		{
			name: "EncodeBatch",
			encode: func() []byte {
				return EncodeBatch(nil)
			},
			wantOp: OpBatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.encode()
			if len(encoded) == 0 {
				t.Fatal("encoded is empty")
			}
			if encoded[0] != tt.wantOp {
				t.Errorf("op byte = %d, want %d", encoded[0], tt.wantOp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge case: Maximum uint16 key length
// ---------------------------------------------------------------------------

func TestMVCCPut_LargeKeyLength(t *testing.T) {
	// Use a large key that fits within uint16 range without triggering
	// arithmetic overflow in the decoder's uint16 boundary checks.
	const keySize = 32000
	key := bytes.Repeat([]byte("A"), keySize)
	value := []byte("val")
	var leaseID int64 = 7

	encoded := EncodeMVCCPut(key, value, leaseID)
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	gotKey, gotValue, gotLease, err := DecodeMVCCPut(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeMVCCPut: %v", err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Errorf("key length = %d, want %d", len(gotKey), len(key))
	}
	if !bytes.Equal(gotValue, value) {
		t.Errorf("value = %q, want %q", gotValue, value)
	}
	if gotLease != leaseID {
		t.Errorf("leaseID = %d, want %d", gotLease, leaseID)
	}
}

func TestMVCCDelete_LargeKeyLength(t *testing.T) {
	const keySize = 32000
	key := bytes.Repeat([]byte("B"), keySize)
	encoded := EncodeMVCCDelete(key)
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	gotKey, err := DecodeMVCCDelete(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeMVCCDelete: %v", err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Errorf("key length = %d, want %d", len(gotKey), len(key))
	}
}

// ---------------------------------------------------------------------------
// Edge case: EncodeOp with nil slices
// ---------------------------------------------------------------------------

func TestEncodeOp_NilSlices(t *testing.T) {
	encoded := EncodeOp(OpMVCCPut, nil, nil, nil, 0)
	op, n, err := DecodeOp(encoded)
	if err != nil {
		t.Fatalf("DecodeOp: %v", err)
	}
	if n != len(encoded) {
		t.Errorf("consumed = %d, want %d", n, len(encoded))
	}
	if op.Type != OpMVCCPut {
		t.Errorf("Type = %d, want %d", op.Type, OpMVCCPut)
	}
	if len(op.Key) != 0 {
		t.Errorf("Key len = %d, want 0", len(op.Key))
	}
	if len(op.Value) != 0 {
		t.Errorf("Value len = %d, want 0", len(op.Value))
	}
	if len(op.RangeEnd) != 0 {
		t.Errorf("RangeEnd len = %d, want 0", len(op.RangeEnd))
	}
}

// ---------------------------------------------------------------------------
// Edge case: EncodeCompare with nil slices
// ---------------------------------------------------------------------------

func TestEncodeCompare_NilSlices(t *testing.T) {
	encoded := EncodeCompare(nil, 0, 0, nil, 0, 0, 0)
	cmp, n, err := DecodeCompare(encoded)
	if err != nil {
		t.Fatalf("DecodeCompare: %v", err)
	}
	if n != len(encoded) {
		t.Errorf("consumed = %d, want %d", n, len(encoded))
	}
	if len(cmp.Key) != 0 {
		t.Errorf("Key len = %d, want 0", len(cmp.Key))
	}
	if len(cmp.Value) != 0 {
		t.Errorf("Value len = %d, want 0", len(cmp.Value))
	}
}

// ---------------------------------------------------------------------------
// Edge case: DecodeCommand with exactly 1 byte (valid, empty payload)
// ---------------------------------------------------------------------------

func TestDecodeCommand_SingleByteValid(t *testing.T) {
	cmd, err := DecodeCommand([]byte{OpBatch})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Op != OpBatch {
		t.Errorf("Op = %d, want %d", cmd.Op, OpBatch)
	}
	if len(cmd.Payload) != 0 {
		t.Errorf("Payload len = %d, want 0", len(cmd.Payload))
	}
}

// ---------------------------------------------------------------------------
// Deep transaction roundtrip: verify all compare and op fields
// ---------------------------------------------------------------------------

func TestTransactionRoundtrip_DeepVerification(t *testing.T) {
	cmp1 := EncodeCompare([]byte("ckey1"), 1, 2, []byte("cval1"), 10, 20, 30)
	cmp2 := EncodeCompare([]byte("ckey2"), 3, 4, []byte("cval2"), 40, 50, 60)
	sOp1 := EncodeOp(OpMVCCPut, []byte("sk1"), []byte("sv1"), []byte("sr1"), 100)
	sOp2 := EncodeOp(OpMVCCDelete, []byte("sk2"), nil, nil, 0)
	fOp1 := EncodeOp(OpMVCCPut, []byte("fk1"), []byte("fv1"), nil, 200)

	encoded := EncodeTransaction([][]byte{cmp1, cmp2}, [][]byte{sOp1, sOp2}, [][]byte{fOp1})
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	txn, err := DecodeTransaction(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeTransaction: %v", err)
	}

	// Verify compares.
	if len(txn.Compares) != 2 {
		t.Fatalf("Compares count = %d, want 2", len(txn.Compares))
	}
	if !bytes.Equal(txn.Compares[0].Key, []byte("ckey1")) {
		t.Errorf("Compares[0].Key = %q, want %q", txn.Compares[0].Key, "ckey1")
	}
	if txn.Compares[0].Target != 1 || txn.Compares[0].Result != 2 {
		t.Errorf("Compares[0] target=%d result=%d, want 1, 2", txn.Compares[0].Target, txn.Compares[0].Result)
	}
	if !bytes.Equal(txn.Compares[0].Value, []byte("cval1")) {
		t.Errorf("Compares[0].Value = %q, want %q", txn.Compares[0].Value, "cval1")
	}
	if txn.Compares[0].Version != 10 || txn.Compares[0].CreateRevision != 20 || txn.Compares[0].ModRevision != 30 {
		t.Errorf("Compares[0] revisions: v=%d cr=%d mr=%d, want 10, 20, 30",
			txn.Compares[0].Version, txn.Compares[0].CreateRevision, txn.Compares[0].ModRevision)
	}

	if !bytes.Equal(txn.Compares[1].Key, []byte("ckey2")) {
		t.Errorf("Compares[1].Key = %q, want %q", txn.Compares[1].Key, "ckey2")
	}
	if txn.Compares[1].Version != 40 || txn.Compares[1].CreateRevision != 50 || txn.Compares[1].ModRevision != 60 {
		t.Errorf("Compares[1] revisions: v=%d cr=%d mr=%d, want 40, 50, 60",
			txn.Compares[1].Version, txn.Compares[1].CreateRevision, txn.Compares[1].ModRevision)
	}

	// Verify success ops.
	if len(txn.SuccessOps) != 2 {
		t.Fatalf("SuccessOps count = %d, want 2", len(txn.SuccessOps))
	}
	if txn.SuccessOps[0].Type != OpMVCCPut {
		t.Errorf("SuccessOps[0].Type = %d, want %d", txn.SuccessOps[0].Type, OpMVCCPut)
	}
	if !bytes.Equal(txn.SuccessOps[0].Key, []byte("sk1")) {
		t.Errorf("SuccessOps[0].Key = %q, want %q", txn.SuccessOps[0].Key, "sk1")
	}
	if !bytes.Equal(txn.SuccessOps[0].Value, []byte("sv1")) {
		t.Errorf("SuccessOps[0].Value = %q, want %q", txn.SuccessOps[0].Value, "sv1")
	}
	if !bytes.Equal(txn.SuccessOps[0].RangeEnd, []byte("sr1")) {
		t.Errorf("SuccessOps[0].RangeEnd = %q, want %q", txn.SuccessOps[0].RangeEnd, "sr1")
	}
	if txn.SuccessOps[0].LeaseID != 100 {
		t.Errorf("SuccessOps[0].LeaseID = %d, want 100", txn.SuccessOps[0].LeaseID)
	}
	if txn.SuccessOps[1].Type != OpMVCCDelete {
		t.Errorf("SuccessOps[1].Type = %d, want %d", txn.SuccessOps[1].Type, OpMVCCDelete)
	}

	// Verify failure ops.
	if len(txn.FailureOps) != 1 {
		t.Fatalf("FailureOps count = %d, want 1", len(txn.FailureOps))
	}
	if txn.FailureOps[0].Type != OpMVCCPut {
		t.Errorf("FailureOps[0].Type = %d, want %d", txn.FailureOps[0].Type, OpMVCCPut)
	}
	if !bytes.Equal(txn.FailureOps[0].Key, []byte("fk1")) {
		t.Errorf("FailureOps[0].Key = %q, want %q", txn.FailureOps[0].Key, "fk1")
	}
	if txn.FailureOps[0].LeaseID != 200 {
		t.Errorf("FailureOps[0].LeaseID = %d, want 200", txn.FailureOps[0].LeaseID)
	}
}

// ---------------------------------------------------------------------------
// Batch roundtrip: deep field verification
// ---------------------------------------------------------------------------

func TestBatchRoundtrip_DeepVerification(t *testing.T) {
	op1 := EncodeOp(OpMVCCPut, []byte("bk1"), []byte("bv1"), []byte("br1"), 11)
	op2 := EncodeOp(OpMVCCDelete, []byte("bk2"), nil, nil, 0)

	encoded := EncodeBatch([][]byte{op1, op2})
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	ops, err := DecodeBatch(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeBatch: %v", err)
	}

	if len(ops) != 2 {
		t.Fatalf("ops count = %d, want 2", len(ops))
	}

	if ops[0].Type != OpMVCCPut {
		t.Errorf("ops[0].Type = %d, want %d", ops[0].Type, OpMVCCPut)
	}
	if !bytes.Equal(ops[0].Key, []byte("bk1")) {
		t.Errorf("ops[0].Key = %q, want %q", ops[0].Key, "bk1")
	}
	if !bytes.Equal(ops[0].Value, []byte("bv1")) {
		t.Errorf("ops[0].Value = %q, want %q", ops[0].Value, "bv1")
	}
	if !bytes.Equal(ops[0].RangeEnd, []byte("br1")) {
		t.Errorf("ops[0].RangeEnd = %q, want %q", ops[0].RangeEnd, "br1")
	}
	if ops[0].LeaseID != 11 {
		t.Errorf("ops[0].LeaseID = %d, want 11", ops[0].LeaseID)
	}

	if ops[1].Type != OpMVCCDelete {
		t.Errorf("ops[1].Type = %d, want %d", ops[1].Type, OpMVCCDelete)
	}
	if !bytes.Equal(ops[1].Key, []byte("bk2")) {
		t.Errorf("ops[1].Key = %q, want %q", ops[1].Key, "bk2")
	}
	if ops[1].LeaseID != 0 {
		t.Errorf("ops[1].LeaseID = %d, want 0", ops[1].LeaseID)
	}
}

// ---------------------------------------------------------------------------
// TSWrite roundtrip with single-tag deterministic verification
// ---------------------------------------------------------------------------

func TestTSWriteRoundtrip_SingleTag(t *testing.T) {
	tags := map[string]string{"host": "server-42"}
	encoded := EncodeTSWrite("latency.p99", 1700000000, 2.718, tags)
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	params, err := DecodeTSWrite(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeTSWrite: %v", err)
	}

	if params.Metric != "latency.p99" {
		t.Errorf("Metric = %q, want %q", params.Metric, "latency.p99")
	}
	if params.Timestamp != 1700000000 {
		t.Errorf("Timestamp = %d, want 1700000000", params.Timestamp)
	}
	if params.Value != 2.718 {
		t.Errorf("Value = %v, want 2.718", params.Value)
	}
	if len(params.Tags) != 1 {
		t.Fatalf("Tags count = %d, want 1", len(params.Tags))
	}
	if params.Tags["host"] != "server-42" {
		t.Errorf("Tags[host] = %q, want %q", params.Tags["host"], "server-42")
	}
}

// ---------------------------------------------------------------------------
// Encode buffer sizes: verify exact encoded sizes
// ---------------------------------------------------------------------------

func TestEncodeMVCCPut_BufferSize(t *testing.T) {
	key := []byte("key")
	value := []byte("value")
	var leaseID int64 = 1
	encoded := EncodeMVCCPut(key, value, leaseID)
	// Expected: 1 (op) + 2 (keyLen) + 3 (key) + 8 (leaseID) + 5 (value) = 19
	if len(encoded) != 19 {
		t.Errorf("encoded length = %d, want 19", len(encoded))
	}
}

func TestEncodeMVCCDelete_BufferSize(t *testing.T) {
	key := []byte("key")
	encoded := EncodeMVCCDelete(key)
	// Expected: 1 (op) + 2 (keyLen) + 3 (key) = 6
	if len(encoded) != 6 {
		t.Errorf("encoded length = %d, want 6", len(encoded))
	}
}

func TestEncodeMVCCRange_BufferSize(t *testing.T) {
	start := []byte("ab")
	end := []byte("cd")
	encoded := EncodeMVCCRange(start, end, 0, 0, false, false)
	// Expected: 1 (op) + 2 (startLen) + 2 (start) + 2 (endLen) + 2 (end) + 8 (rev) + 8 (limit) + 1 (flags) = 26
	if len(encoded) != 26 {
		t.Errorf("encoded length = %d, want 26", len(encoded))
	}
}

func TestEncodeMVCCCompact_BufferSize(t *testing.T) {
	encoded := EncodeMVCCCompact(0)
	// Expected: 1 (op) + 8 (revision) = 9
	if len(encoded) != 9 {
		t.Errorf("encoded length = %d, want 9", len(encoded))
	}
}

func TestEncodeLeaseGrant_BufferSize(t *testing.T) {
	encoded := EncodeLeaseGrant(0)
	if len(encoded) != 9 {
		t.Errorf("encoded length = %d, want 9", len(encoded))
	}
}

func TestEncodeLeaseRevoke_BufferSize(t *testing.T) {
	encoded := EncodeLeaseRevoke(0)
	if len(encoded) != 9 {
		t.Errorf("encoded length = %d, want 9", len(encoded))
	}
}

func TestEncodeLeaseKeepAlive_BufferSize(t *testing.T) {
	encoded := EncodeLeaseKeepAlive(0)
	if len(encoded) != 9 {
		t.Errorf("encoded length = %d, want 9", len(encoded))
	}
}

func TestEncodeIndexCreate_BufferSize(t *testing.T) {
	encoded := EncodeIndexCreate("idx", "fld", false, false, false)
	// Expected: 1 (op) + 2 (nameLen) + 3 (name) + 2 (fieldLen) + 3 (field) + 1 (flags) = 12
	if len(encoded) != 12 {
		t.Errorf("encoded length = %d, want 12", len(encoded))
	}
}

func TestEncodeIndexDrop_BufferSize(t *testing.T) {
	encoded := EncodeIndexDrop("idx")
	// Expected: 1 (op) + 2 (nameLen) + 3 (name) = 6
	if len(encoded) != 6 {
		t.Errorf("encoded length = %d, want 6", len(encoded))
	}
}

func TestEncodeTSWrite_BufferSize(t *testing.T) {
	encoded := EncodeTSWrite("cpu", 0, 0.0, nil)
	// Expected: 1 (op) + 2 (metricLen) + 3 (metric) + 8 (ts) + 8 (val) + 2 (tagCount) = 24
	if len(encoded) != 24 {
		t.Errorf("encoded length = %d, want 24", len(encoded))
	}
}

func TestEncodeOp_BufferSize(t *testing.T) {
	encoded := EncodeOp(OpMVCCPut, []byte("k"), []byte("v"), []byte("e"), 0)
	// Expected: 1 (type) + 2 (keyLen) + 1 (key) + 2 (valLen) + 1 (val) + 2 (reLen) + 1 (re) + 8 (leaseID) = 18
	if len(encoded) != 18 {
		t.Errorf("encoded length = %d, want 18", len(encoded))
	}
}

func TestEncodeCompare_BufferSize(t *testing.T) {
	encoded := EncodeCompare([]byte("k"), 0, 0, []byte("v"), 0, 0, 0)
	// Expected: 2 (keyLen) + 1 (key) + 1 (target) + 1 (result) + 2 (valLen) + 1 (val) + 8 + 8 + 8 = 32
	if len(encoded) != 32 {
		t.Errorf("encoded length = %d, want 32", len(encoded))
	}
}

// ---------------------------------------------------------------------------
// Range flag encoding correctness
// ---------------------------------------------------------------------------

func TestMVCCRange_FlagEncoding(t *testing.T) {
	tests := []struct {
		name      string
		countOnly bool
		keysOnly  bool
		wantFlag  byte
	}{
		{name: "no flags", countOnly: false, keysOnly: false, wantFlag: 0},
		{name: "count only", countOnly: true, keysOnly: false, wantFlag: RangeFlagCountOnly},
		{name: "keys only", countOnly: false, keysOnly: true, wantFlag: RangeFlagKeysOnly},
		{name: "both flags", countOnly: true, keysOnly: true, wantFlag: RangeFlagCountOnly | RangeFlagKeysOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeMVCCRange([]byte("a"), []byte("z"), 0, 0, tt.countOnly, tt.keysOnly)
			// Flags byte is the last byte.
			gotFlag := encoded[len(encoded)-1]
			if gotFlag != tt.wantFlag {
				t.Errorf("flags = %d, want %d", gotFlag, tt.wantFlag)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Index flag encoding correctness
// ---------------------------------------------------------------------------

func TestIndexCreate_FlagEncoding(t *testing.T) {
	tests := []struct {
		name     string
		unique   bool
		sparse   bool
		numeric  bool
		wantFlag byte
	}{
		{name: "no flags", unique: false, sparse: false, numeric: false, wantFlag: 0},
		{name: "unique only", unique: true, sparse: false, numeric: false, wantFlag: IndexFlagUnique},
		{name: "sparse only", unique: false, sparse: true, numeric: false, wantFlag: IndexFlagSparse},
		{name: "both flags", unique: true, sparse: true, numeric: false, wantFlag: IndexFlagUnique | IndexFlagSparse},
		{name: "numeric only", unique: false, sparse: false, numeric: true, wantFlag: IndexFlagNumeric},
		{name: "unique+numeric", unique: true, sparse: false, numeric: true, wantFlag: IndexFlagUnique | IndexFlagNumeric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeIndexCreate("n", "f", tt.unique, tt.sparse, tt.numeric)
			// Flags byte is the last byte.
			gotFlag := encoded[len(encoded)-1]
			if gotFlag != tt.wantFlag {
				t.Errorf("flags = %d, want %d", gotFlag, tt.wantFlag)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TSWrite edge: very large float64 values
// ---------------------------------------------------------------------------

func TestTSWrite_SmallestPositiveFloat(t *testing.T) {
	val := math.SmallestNonzeroFloat64
	encoded := EncodeTSWrite("tiny", 0, val, nil)
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}
	params, err := DecodeTSWrite(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeTSWrite: %v", err)
	}
	if params.Value != val {
		t.Errorf("Value = %v, want %v", params.Value, val)
	}
}

func TestTSWrite_MaxFloat64(t *testing.T) {
	val := math.MaxFloat64
	encoded := EncodeTSWrite("huge", 0, val, nil)
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}
	params, err := DecodeTSWrite(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeTSWrite: %v", err)
	}
	if params.Value != val {
		t.Errorf("Value = %v, want %v", params.Value, val)
	}
}

// ---------------------------------------------------------------------------
// DecodeOp consumed bytes verification
// ---------------------------------------------------------------------------

func TestDecodeOp_ConsumedBytes(t *testing.T) {
	encoded := EncodeOp(OpMVCCPut, []byte("key"), []byte("value"), []byte("end"), 42)
	// Append extra garbage bytes to simulate reading from a larger buffer.
	withTrailing := append(encoded, 0xDE, 0xAD, 0xBE, 0xEF)

	op, n, err := DecodeOp(withTrailing)
	if err != nil {
		t.Fatalf("DecodeOp: %v", err)
	}
	if n != len(encoded) {
		t.Errorf("consumed = %d, want %d", n, len(encoded))
	}
	if op.Type != OpMVCCPut {
		t.Errorf("Type = %d, want %d", op.Type, OpMVCCPut)
	}
}

func TestDecodeCompare_ConsumedBytes(t *testing.T) {
	encoded := EncodeCompare([]byte("ckey"), 5, 6, []byte("cval"), 100, 200, 300)
	withTrailing := append(encoded, 0xCA, 0xFE)

	cmp, n, err := DecodeCompare(withTrailing)
	if err != nil {
		t.Fatalf("DecodeCompare: %v", err)
	}
	if n != len(encoded) {
		t.Errorf("consumed = %d, want %d", n, len(encoded))
	}
	if !bytes.Equal(cmp.Key, []byte("ckey")) {
		t.Errorf("Key = %q, want %q", cmp.Key, "ckey")
	}
}

// ---------------------------------------------------------------------------
// Time-Series WriteBatch encode/decode
// ---------------------------------------------------------------------------

func TestEncodeTSWriteBatch_RoundTrip(t *testing.T) {
	points := []TSWriteParams{
		{Metric: "cpu.usage", Timestamp: 1000000, Value: 42.5, Tags: map[string]string{"host": "node1"}},
		{Metric: "mem.free", Timestamp: 2000000, Value: 1024.0, Tags: nil},
	}

	encoded := EncodeTSWriteBatch(points)
	if encoded[0] != OpTSWriteBatch {
		t.Fatalf("opcode = 0x%02x, want 0x%02x", encoded[0], OpTSWriteBatch)
	}

	decoded, err := DecodeTSWriteBatch(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSWriteBatch: %v", err)
	}
	if len(decoded) != len(points) {
		t.Fatalf("decoded %d points, want %d", len(decoded), len(points))
	}

	if decoded[0].Metric != "cpu.usage" {
		t.Errorf("decoded[0].Metric = %q, want %q", decoded[0].Metric, "cpu.usage")
	}
	if decoded[0].Timestamp != 1000000 {
		t.Errorf("decoded[0].Timestamp = %d, want 1000000", decoded[0].Timestamp)
	}
	if decoded[0].Value != 42.5 {
		t.Errorf("decoded[0].Value = %f, want 42.5", decoded[0].Value)
	}
	if decoded[0].Tags["host"] != "node1" {
		t.Errorf("decoded[0].Tags[host] = %q, want %q", decoded[0].Tags["host"], "node1")
	}

	if decoded[1].Metric != "mem.free" {
		t.Errorf("decoded[1].Metric = %q, want %q", decoded[1].Metric, "mem.free")
	}
	if decoded[1].Value != 1024.0 {
		t.Errorf("decoded[1].Value = %f, want 1024.0", decoded[1].Value)
	}
}

func TestEncodeTSWriteBatch_EmptyPoints(t *testing.T) {
	encoded := EncodeTSWriteBatch(nil)
	decoded, err := DecodeTSWriteBatch(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSWriteBatch: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("decoded %d points, want 0", len(decoded))
	}
}

func TestDecodeTSWriteBatch_PayloadTooShort(t *testing.T) {
	_, err := DecodeTSWriteBatch([]byte{0x01})
	if err == nil {
		t.Fatal("expected error for short payload")
	}
	var cmdErr *InvalidCommandError
	if !errors.As(err, &cmdErr) {
		t.Errorf("expected InvalidCommandError, got %T", err)
	}
}

func TestDecodeTSWriteBatch_TruncatedPointLength(t *testing.T) {
	// Encode 2 points claimed but only provide data for first
	points := []TSWriteParams{
		{Metric: "cpu", Timestamp: 1000, Value: 1.0, Tags: nil},
	}
	encoded := EncodeTSWriteBatch(points)
	// Overwrite point count to 2 but don't add second point
	encoded[1] = 2
	encoded[2] = 0
	_, err := DecodeTSWriteBatch(encoded[1:])
	if err == nil {
		t.Fatal("expected error for truncated point")
	}
}

// ---------------------------------------------------------------------------
// Time-Series WritePoint encode/decode
// ---------------------------------------------------------------------------

func TestEncodeTSWritePoint_RoundTrip(t *testing.T) {
	encoded := EncodeTSWritePoint("disk.io", 5000000, 99.9, map[string]string{"dev": "sda1"})
	if encoded[0] != OpTSWritePoint {
		t.Fatalf("opcode = 0x%02x, want 0x%02x", encoded[0], OpTSWritePoint)
	}

	// Decode the payload (after opcode) using DecodeTSWrite
	decoded, err := DecodeTSWrite(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSWrite: %v", err)
	}
	if decoded.Metric != "disk.io" {
		t.Errorf("Metric = %q, want %q", decoded.Metric, "disk.io")
	}
	if decoded.Timestamp != 5000000 {
		t.Errorf("Timestamp = %d, want 5000000", decoded.Timestamp)
	}
	if decoded.Value != 99.9 {
		t.Errorf("Value = %f, want 99.9", decoded.Value)
	}
	if decoded.Tags["dev"] != "sda1" {
		t.Errorf("Tags[dev] = %q, want %q", decoded.Tags["dev"], "sda1")
	}
}

func TestEncodeTSWritePoint_NoTags(t *testing.T) {
	encoded := EncodeTSWritePoint("net.rx", 1234, 100.0, nil)
	decoded, err := DecodeTSWrite(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSWrite: %v", err)
	}
	if decoded.Metric != "net.rx" {
		t.Errorf("Metric = %q, want %q", decoded.Metric, "net.rx")
	}
	if len(decoded.Tags) != 0 {
		t.Errorf("expected 0 tags, got %d", len(decoded.Tags))
	}
}

// ---------------------------------------------------------------------------
// Time-Series DeleteSeries encode/decode
// ---------------------------------------------------------------------------

func TestEncodeTSDeleteSeries_RoundTrip(t *testing.T) {
	tags := map[string]string{"region": "us-east", "env": "prod"}
	encoded := EncodeTSDeleteSeries("cpu.usage", 1000, 2000, tags)

	if encoded[0] != OpTSDeleteSeries {
		t.Fatalf("opcode = 0x%02x, want 0x%02x", encoded[0], OpTSDeleteSeries)
	}

	decoded, err := DecodeTSDeleteSeries(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSDeleteSeries: %v", err)
	}
	if decoded.Metric != "cpu.usage" {
		t.Errorf("Metric = %q, want %q", decoded.Metric, "cpu.usage")
	}
	if decoded.StartNs != 1000 {
		t.Errorf("StartNs = %d, want 1000", decoded.StartNs)
	}
	if decoded.EndNs != 2000 {
		t.Errorf("EndNs = %d, want 2000", decoded.EndNs)
	}
	if len(decoded.Tags) != 2 {
		t.Fatalf("tag count = %d, want 2", len(decoded.Tags))
	}
	if decoded.Tags["region"] != "us-east" {
		t.Errorf("Tags[region] = %q, want %q", decoded.Tags["region"], "us-east")
	}
	if decoded.Tags["env"] != "prod" {
		t.Errorf("Tags[env] = %q, want %q", decoded.Tags["env"], "prod")
	}
}

func TestEncodeTSDeleteSeries_NoTags(t *testing.T) {
	encoded := EncodeTSDeleteSeries("mem.free", 0, 9999, nil)
	decoded, err := DecodeTSDeleteSeries(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSDeleteSeries: %v", err)
	}
	if decoded.Metric != "mem.free" {
		t.Errorf("Metric = %q, want %q", decoded.Metric, "mem.free")
	}
	if len(decoded.Tags) != 0 {
		t.Errorf("expected 0 tags, got %d", len(decoded.Tags))
	}
}

func TestDecodeTSDeleteSeries_PayloadTooShort(t *testing.T) {
	_, err := DecodeTSDeleteSeries([]byte{0x01})
	if err == nil {
		t.Fatal("expected error for short payload")
	}
	var cmdErr *InvalidCommandError
	if !errors.As(err, &cmdErr) {
		t.Errorf("expected InvalidCommandError, got %T", err)
	}
}

func TestDecodeTSDeleteSeries_MetricExceedsPayload(t *testing.T) {
	// Claim metric length 100 but only provide 5 bytes total
	payload := make([]byte, 5)
	payload[0] = 0x64 // metric len = 100 LE
	_, err := DecodeTSDeleteSeries(payload)
	if err == nil {
		t.Fatal("expected error for metric exceeding payload")
	}
}

func TestDecodeTSDeleteSeries_MissingTimestamps(t *testing.T) {
	// Valid metric but missing startNs/endNs/tagCount
	metric := "cpu"
	payload := make([]byte, 2+len(metric))
	payload[0] = byte(len(metric))
	payload[1] = 0
	copy(payload[2:], metric)
	_, err := DecodeTSDeleteSeries(payload)
	if err == nil {
		t.Fatal("expected error for missing timestamps")
	}
}

func TestDecodeTSDeleteSeries_TruncatedTagKey(t *testing.T) {
	// Build a valid payload up to tags, then claim 1 tag but truncate
	encoded := EncodeTSDeleteSeries("m", 0, 0, map[string]string{"k": "v"})
	// Truncate payload to cut into tag section
	truncated := encoded[1 : len(encoded)-3]
	_, err := DecodeTSDeleteSeries(truncated)
	if err == nil {
		t.Fatal("expected error for truncated tag")
	}
}

// ---------------------------------------------------------------------------
// Time-Series SetRetention encode/decode
// ---------------------------------------------------------------------------

func TestEncodeTSSetRetention_RoundTrip(t *testing.T) {
	encoded := EncodeTSSetRetention("cpu.usage", 86400000000000, 60000000000, 1)

	if encoded[0] != OpTSSetRetention {
		t.Fatalf("opcode = 0x%02x, want 0x%02x", encoded[0], OpTSSetRetention)
	}

	decoded, err := DecodeTSSetRetention(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSSetRetention: %v", err)
	}
	if decoded.Metric != "cpu.usage" {
		t.Errorf("Metric = %q, want %q", decoded.Metric, "cpu.usage")
	}
	if decoded.DurationNs != 86400000000000 {
		t.Errorf("DurationNs = %d, want 86400000000000", decoded.DurationNs)
	}
	if decoded.ResolutionNs != 60000000000 {
		t.Errorf("ResolutionNs = %d, want 60000000000", decoded.ResolutionNs)
	}
	if decoded.Aggregation != 1 {
		t.Errorf("Aggregation = %d, want 1", decoded.Aggregation)
	}
}

func TestDecodeTSSetRetention_PayloadTooShort(t *testing.T) {
	_, err := DecodeTSSetRetention([]byte{0x01})
	if err == nil {
		t.Fatal("expected error for short payload")
	}
	var cmdErr *InvalidCommandError
	if !errors.As(err, &cmdErr) {
		t.Errorf("expected InvalidCommandError, got %T", err)
	}
}

func TestDecodeTSSetRetention_MetricExceedsPayload(t *testing.T) {
	payload := make([]byte, 5)
	payload[0] = 0x64 // metric len = 100 LE
	_, err := DecodeTSSetRetention(payload)
	if err == nil {
		t.Fatal("expected error for metric exceeding payload")
	}
}

func TestDecodeTSSetRetention_MissingFields(t *testing.T) {
	// Valid metric but missing duration/resolution/aggregation fields
	metric := "cpu"
	payload := make([]byte, 2+len(metric))
	payload[0] = byte(len(metric))
	payload[1] = 0
	copy(payload[2:], metric)
	_, err := DecodeTSSetRetention(payload)
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
}

func TestEncodeTSWriteBatch_MultipleTags(t *testing.T) {
	tags := map[string]string{"host": "a", "dc": "us1", "env": "prod"}
	points := []TSWriteParams{
		{Metric: "m1", Timestamp: 100, Value: math.Pi, Tags: tags},
	}
	encoded := EncodeTSWriteBatch(points)
	decoded, err := DecodeTSWriteBatch(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSWriteBatch: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded %d points, want 1", len(decoded))
	}
	if len(decoded[0].Tags) != 3 {
		t.Fatalf("tag count = %d, want 3", len(decoded[0].Tags))
	}
	if decoded[0].Value != math.Pi {
		t.Errorf("Value = %v, want %v", decoded[0].Value, math.Pi)
	}
}

func TestEncodeTSSetRetention_EmptyMetric(t *testing.T) {
	encoded := EncodeTSSetRetention("", 1000, 100, 0)
	decoded, err := DecodeTSSetRetention(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSSetRetention: %v", err)
	}
	if decoded.Metric != "" {
		t.Errorf("Metric = %q, want empty", decoded.Metric)
	}
}

func TestEncodeTSDeleteSeries_LargeTimestamps(t *testing.T) {
	encoded := EncodeTSDeleteSeries("metric", math.MinInt64, math.MaxInt64, nil)
	decoded, err := DecodeTSDeleteSeries(encoded[1:])
	if err != nil {
		t.Fatalf("DecodeTSDeleteSeries: %v", err)
	}
	if decoded.StartNs != math.MinInt64 {
		t.Errorf("StartNs = %d, want MinInt64", decoded.StartNs)
	}
	if decoded.EndNs != math.MaxInt64 {
		t.Errorf("EndNs = %d, want MaxInt64", decoded.EndNs)
	}
}
