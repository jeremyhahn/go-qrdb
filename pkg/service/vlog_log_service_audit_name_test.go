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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// countingKeyProvider counts the keys a log service asks it to mint.
type countingKeyProvider struct {
	staticSignerProvider
	creates int
}

func (p *countingKeyProvider) CreateKey(ctx context.Context, req vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	p.creates++
	return p.staticSignerProvider.CreateKey(ctx, req)
}

// auditNameRef names a tenant's system/audit log.
var auditNameRef = LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}

// TestLogService_CreateLog_RefusesTheAuditName: the audit name belongs to
// the audit instance, so a public create of system/audit is refused before
// a key is minted or a command proposed, with or without a signing key.
func TestLogService_CreateLog_RefusesTheAuditName(t *testing.T) {
	for name, keyType := range map[string]vlog.SignatureType{
		"without a signing key": "",
		"with a signing key":    vlog.SignatureEd25519,
	} {
		t.Run(name, func(t *testing.T) {
			host, cmds := recordingHost(sm.Result{})
			keys := &countingKeyProvider{staticSignerProvider: staticSignerProvider{created: vlog.CreatedKey{Ref: "hsm:1", Public: newHSMProviderKey(t)}}}
			svc := newTestLogService(t, host, keys)
			_, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: auditNameRef, Origin: "o", SigningKeyType: keyType})
			var reserved *vlog.ReservedLogError
			if !errors.As(err, &reserved) || reserved.ID != vlog.AuditLogID("acme") {
				t.Fatalf("err = %v, want ReservedLogError for acme/system/audit", err)
			}
			if keys.creates != 0 {
				t.Fatalf("a refused create minted %d keys", keys.creates)
			}
			if len(*cmds) != 0 {
				t.Fatalf("a refused create proposed %d commands", len(*cmds))
			}
		})
	}
}

// TestLogService_Append_LegacyAuditNameLogStaysWritable: a plain log created
// under system/audit before the audit instance existed has a descriptor
// without Audit; it keeps its own entries, so a client's append to it is
// proposed as to any named log.
func TestLogService_Append_LegacyAuditNameLogStaysWritable(t *testing.T) {
	host, cmds := recordingHost(sm.Result{Value: 3})
	host.syncReadFn = func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
		if r, ok := q.(vlog.DescriptorRequest); ok && r.ID == vlog.AuditLogID("acme") {
			return &vlog.Descriptor{Origin: "o", Hash: vlog.HashSHA256}, nil
		}
		return nil, errors.New("unexpected read")
	}
	svc := newTestLogService(t, host, nil)
	resp, err := svc.Append(acmeCtx(), &AppendRequest{LogRef: auditNameRef, Entries: [][]byte{[]byte("own")}})
	if err != nil || resp.FirstIndex != 3 || resp.Count != 1 {
		t.Fatalf("Append = %+v, %v; want first index 3, count 1", resp, err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("Append proposed %d commands, want 1", len(*cmds))
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	id, entries, err := protocol.DecodeVerifiableLogAppend(tenantID, body)
	if err != nil || id != vlog.AuditLogID("acme") || len(entries) != 1 || string(entries[0]) != "own" {
		t.Fatalf("decoded append = %v %q, %v; want the legacy log's entry", id, entries, err)
	}
}
