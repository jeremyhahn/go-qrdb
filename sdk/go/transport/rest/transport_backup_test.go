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

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTBackup_SendsTheDocumentedRequest pins each backup RPC to its verb,
// path and body on the wire, and checks the wire answer is converted into the
// transport type: the RFC 3339 timestamp parsed, an unparsable one left zero,
// duration_ms read as milliseconds, and the key provider carried through.
func TestRESTBackup_SendsTheDocumentedRequest(t *testing.T) {
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	create := &transport.BackupRequest{Destination: "/backups/daily", KeyRef: "kek"}
	restore := &transport.RestoreRequest{BackupID: "bk-001", ValidateFirst: true}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "CreateBackup", verb: http.MethodPost, path: "/v1/backups", wantBody: create,
			answer: restBackupInfo{ID: "bk-001", Timestamp: ts.Format(time.RFC3339), Size: 1024, DurationMs: 250,
				Encryption: "aes256", KeyProviderID: "software"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.CreateBackup(ctx, create)
				if err == nil && (info.ID != "bk-001" || info.Size != 1024 || !info.Timestamp.Equal(ts) ||
					info.Duration != 250*time.Millisecond || info.KeyProviderID != "software") {
					return errors.New("backup info not converted: " + info.ID)
				}
				return err
			}},
		{name: "ListBackups", verb: http.MethodGet, path: "/v1/backups/list",
			answer: restListBackupsResponse{Backups: []restBackupInfo{{ID: "bk-001"}, {ID: "bk-002"}}, Total: 2},
			callCheck: func(ctx context.Context, rt *Transport) error {
				backups, err := rt.ListBackups(ctx)
				if err == nil && (len(backups) != 2 || backups[0].ID != "bk-001" || backups[1].ID != "bk-002") {
					return errors.New("backups not decoded, want bk-001 and bk-002")
				}
				return err
			}},
		{name: "GetBackup/UnparsableTimestamp", verb: http.MethodGet, path: "/v1/backups/bk-007",
			answer: restBackupInfo{ID: "bk-007", Timestamp: "not-a-time"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.GetBackup(ctx, "bk-007")
				if err == nil && (info.ID != "bk-007" || !info.Timestamp.IsZero() || info.KeyProviderID != "") {
					return errors.New("want bk-007 with a zero timestamp and no key provider")
				}
				return err
			}},
		{name: "DeleteBackup", verb: http.MethodDelete, path: "/v1/backups/bk-001", status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.DeleteBackup(ctx, "bk-001")
			}},
		{name: "RestoreBackup", verb: http.MethodPost, path: "/v1/backups/restore", wantBody: restore,
			answer: map[string]any{"backup_id": "bk-001", "shards_restored": 2, "duration_ms": 500,
				"errors": []map[string]any{{"shard_id": 3, "message": "shard 3 offline"}}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				res, err := rt.RestoreBackup(ctx, restore)
				if err == nil && (res.BackupID != "bk-001" || res.ShardsRestored != 2 || res.Duration != 500*time.Millisecond ||
					len(res.Errors) != 1 || res.Errors[0].ShardID != 3 || res.Errors[0].Message != "shard 3 offline") {
					return errors.New("restore result not decoded, want bk-001 with shard 3 failed")
				}
				return err
			}},
		{name: "VerifyBackup", verb: http.MethodPost, path: "/v1/backups/bk-001/verify", wantBody: struct{}{},
			answer: transport.VerifyResult{Valid: true, ChecksumMatch: true},
			callCheck: func(ctx context.Context, rt *Transport) error {
				res, err := rt.VerifyBackup(ctx, "bk-001")
				if err == nil && (!res.Valid || !res.ChecksumMatch) {
					return errors.New("verify result not decoded, want valid")
				}
				return err
			}},
	})
}

// TestRESTBackup_SurfacesAFailedAnswer checks every backup RPC surfaces a
// refused or undecodable answer as a RequestError naming it.
func TestRESTBackup_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"CreateBackup", http.MethodPost, "/v1/backups", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateBackup(ctx, &transport.BackupRequest{})
			return err
		}},
		{"ListBackups", http.MethodGet, "/v1/backups/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListBackups(ctx)
			return err
		}},
		{"GetBackup", http.MethodGet, "/v1/backups/bk-001", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetBackup(ctx, "bk-001")
			return err
		}},
		{"DeleteBackup", http.MethodDelete, "/v1/backups/bk-001", false, func(ctx context.Context, rt *Transport) error {
			return rt.DeleteBackup(ctx, "bk-001")
		}},
		{"RestoreBackup", http.MethodPost, "/v1/backups/restore", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.RestoreBackup(ctx, &transport.RestoreRequest{})
			return err
		}},
		{"VerifyBackup", http.MethodPost, "/v1/backups/bk-001/verify", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.VerifyBackup(ctx, "bk-001")
			return err
		}},
	})
}

// TestRESTBackup_Closed checks every backup RPC refuses on a closed transport
// before anything reaches the wire.
func TestRESTBackup_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"CreateBackup": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateBackup(ctx, &transport.BackupRequest{})
			return err
		},
		"ListBackups": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListBackups(ctx)
			return err
		},
		"GetBackup": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetBackup(ctx, "bk-001")
			return err
		},
		"DeleteBackup": func(ctx context.Context, rt *Transport) error {
			return rt.DeleteBackup(ctx, "bk-001")
		},
		"RestoreBackup": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RestoreBackup(ctx, &transport.RestoreRequest{})
			return err
		},
		"VerifyBackup": func(ctx context.Context, rt *Transport) error {
			_, err := rt.VerifyBackup(ctx, "bk-001")
			return err
		},
	})
}
