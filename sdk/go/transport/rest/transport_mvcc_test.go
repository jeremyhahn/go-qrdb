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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTMVCC_SendsTheDocumentedRequest pins, per MVCC RPC, the verb, path,
// query and body the transport puts on the wire, and that the answer decodes
// into the value the RPC returns.
func TestRESTMVCC_SendsTheDocumentedRequest(t *testing.T) {
	const kv = `{"key":"aw==","value":"dg==","mod_revision":7,"lease_id":3}`
	compactBody, err := json.Marshal(mvccCompactWireRequest{Revision: 9})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	showKV := func(kv *transport.MVCCKeyValue) string {
		return fmt.Sprintf("%s=%s@%d/%d", kv.Key, kv.Value, kv.ModRevision, kv.LeaseID)
	}
	rows := []struct {
		name, verb, path, query, body, answer string
		call                                  func(context.Context, *Transport) (string, error)
		want                                  string
	}{
		{"PutWithLease", http.MethodPut, "/kv/k", "lease-id=3", "v", kv,
			func(ctx context.Context, rt *Transport) (string, error) {
				got, err := rt.PutWithLease(ctx, "k", []byte("v"), 3)
				if err != nil {
					return "", err
				}
				return showKV(got), nil
			}, "k=v@7/3"},
		{"GetRevision", http.MethodGet, "/kv/k", "revision=true", "", kv,
			func(ctx context.Context, rt *Transport) (string, error) {
				got, err := rt.GetRevision(ctx, "k")
				if err != nil {
					return "", err
				}
				return showKV(got), nil
			}, "k=v@7/3"},
		{"GetAtRevision", http.MethodGet, "/kv/k", "at-revision=5", "", kv,
			func(ctx context.Context, rt *Transport) (string, error) {
				got, err := rt.GetAtRevision(ctx, "k", 5)
				if err != nil {
					return "", err
				}
				return showKV(got), nil
			}, "k=v@7/3"},
		{"Range", http.MethodGet, "/kv/range", "count-only=true&end=z&keys-only=true&limit=10&revision=4&start=a", "",
			`{"kvs":[` + kv + `],"count":1,"revision":8}`,
			func(ctx context.Context, rt *Transport) (string, error) {
				got, err := rt.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10, Revision: 4, CountOnly: true, KeysOnly: true})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%d %d %s", got.Count, got.Revision, showKV(got.Kvs[0])), nil
			}, "1 8 k=v@7/3"},
		{"Compact", http.MethodPost, "/kv/compact", "", string(compactBody), `{}`,
			func(ctx context.Context, rt *Transport) (string, error) {
				return "compacted", rt.Compact(ctx, 9)
			}, "compacted"},
		{"CurrentRevision", http.MethodGet, "/kv/revision", "", "", `{"revision":42}`,
			func(ctx context.Context, rt *Transport) (string, error) {
				rev, err := rt.CurrentRevision(ctx)
				return fmt.Sprint(rev), err
			}, "42"},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			var verb, path, query string
			var body []byte
			rs.handle(func(*http.Request) bool { return true }, func(w http.ResponseWriter, r *http.Request) {
				verb, path, query = r.Method, r.URL.Path, r.URL.RawQuery
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.answer)
			})
			got, err := tc.call(context.Background(), rt)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if verb != tc.verb || path != tc.path || query != tc.query {
				t.Fatalf("request = %s %s?%s, want %s %s?%s", verb, path, query, tc.verb, tc.path, tc.query)
			}
			if string(bytes.TrimSpace(body)) != tc.body {
				t.Fatalf("body = %q, want %q", body, tc.body)
			}
			if got != tc.want {
				t.Fatalf("decoded = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRESTMVCC_RestoreToRevision_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/restore"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{
			"revision": 5,
			"changes": []interface{}{
				map[string]interface{}{"key": "YQ==", "action": "revert", "from_revision": 9, "to_revision": 5, "value_bytes": 3},
			},
			"applied": 1,
		})
	})
	result, err := rt.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Revision != 5 || result.Applied != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "a" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}
}

// TestRESTMVCC_RestoreToRevision_BinaryRoundTrip proves a non-UTF-8 prefix
// reaches the server, and a non-UTF-8 key comes back, byte for byte.
func TestRESTMVCC_RestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryPrefix := []byte{0x00, 0xff, 0xfe, 'p'}
	binaryRespKey := []byte{0x00, 0xff, 0xfe, 'k'}

	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/restore"), func(w http.ResponseWriter, r *http.Request) {
		var req mvccRestoreWireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !bytes.Equal(req.Prefix, binaryPrefix) {
			t.Fatalf("server received prefix %v, want %v (binary corruption)", req.Prefix, binaryPrefix)
		}
		writeJSON(w, mvccRestoreWireResponse{
			Revision: 5,
			Changes: []*mvccRestoreKeyChangeWireResponse{
				{Key: binaryRespKey, Action: transport.RestoreActionRevert, ToRevision: 5},
			},
			Applied: 1,
		})
	})

	result, err := rt.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   binaryPrefix,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Changes) != 1 || !bytes.Equal(result.Changes[0].Key, binaryRespKey) {
		t.Fatalf("expected round-tripped binary response key %v, got %+v", binaryRespKey, result.Changes)
	}
}

func TestRESTMVCC_RestoreToRevision_NilRequestNeverReachesTheServer(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var reached atomic.Bool
	rs.handle(func(*http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		writeJSON(w, struct{}{})
	})
	_, err := rt.RestoreToRevision(context.Background(), nil)
	if !errors.As(err, new(*transport.NilRequestError)) {
		t.Fatalf("RestoreToRevision(nil) = %T %v, want *transport.NilRequestError", err, err)
	}
	if reached.Load() {
		t.Fatal("a nil request reached the server")
	}
}

// TestRESTMVCC_RestoreToRevision_GuardErrorStatus proves the server's refusal
// keeps its status code, so a caller can tell an unsupported state machine
// from a failed restore.
func TestRESTMVCC_RestoreToRevision_GuardErrorStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/restore"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		writeJSON(w, map[string]string{"error": "requires raft.sm_type: mvcc"})
	})

	_, err := rt.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5, Prefix: []byte("k"),
	})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusNotImplemented {
		t.Fatalf("StatusCode = %d, want %d", reqErr.StatusCode, http.StatusNotImplemented)
	}
}

// TestRESTTransaction_SendsTheDocumentedRequest pins the /tx body for every
// compare target and result and every op type, and that each op response's
// key-values decode.
func TestRESTTransaction_SendsTheDocumentedRequest(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var got txWireRequest
	rs.handle(exactPath(http.MethodPost, "/tx"), func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"succeeded": true, "revision": 6, "responses": []interface{}{
			map[string]interface{}{"succeeded": true, "count": 1, "kvs": []interface{}{map[string]interface{}{"key": "aw==", "value": "dg=="}}},
		}})
	})
	resp, err := rt.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k1"), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v")},
			{Key: []byte("k2"), Target: transport.CompareVersion, Result: transport.CompareGreater, Version: 1},
			{Key: []byte("k3"), Target: transport.CompareCreateRevision, Result: transport.CompareLess, CreateRevision: 5},
			{Key: []byte("k4"), Target: transport.CompareModRevision, Result: transport.CompareNotEqual, ModRevision: 7},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1},
			{Type: transport.OpGet, Key: []byte("k")},
			{Type: transport.OpDelete, Key: []byte("k")},
			{Type: transport.OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
		},
		FailureOps: []transport.Op{
			{Type: transport.OpGet, Key: []byte("x")},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	want := txWireRequest{
		Compares: []txWireCompare{
			{Key: "k1", Target: "value", Result: "equal", Value: "v"},
			{Key: "k2", Target: "version", Result: "greater", Version: 1},
			{Key: "k3", Target: "create_revision", Result: "less", CreateRevision: 5},
			{Key: "k4", Target: "mod_revision", Result: "not_equal", ModRevision: 7},
		},
		SuccessOps: []txWireOp{
			{Type: "put", Key: "k", Value: "v", LeaseID: 1},
			{Type: "get", Key: "k"},
			{Type: "delete", Key: "k"},
			{Type: "range", Key: "a", RangeEnd: "z"},
		},
		FailureOps: []txWireOp{{Type: "get", Key: "x"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("server saw %+v\nwant %+v", got, want)
	}
	if !resp.Succeeded || resp.Revision != 6 || len(resp.Responses) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if op := resp.Responses[0]; !op.Succeeded || op.Count != 1 || string(op.Kvs[0].Key) != "k" || string(op.Kvs[0].Value) != "v" {
		t.Fatalf("op response = %+v", op)
	}
}
