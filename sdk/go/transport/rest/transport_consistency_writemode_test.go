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
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// KV Put – ?write_mode query param
// ---------------------------------------------------------------------------

func TestTransportPut_WriteModeSync_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.Put(context.Background(), "k", []byte("v"), transport.WithWriteMode(transport.WriteModeSync)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if capturedMode != "sync" {
		t.Fatalf("write_mode = %q, want %q", capturedMode, "sync")
	}
}

func TestTransportPut_WriteModeBatched_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.Put(context.Background(), "k", []byte("v"), transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if capturedMode != "batched" {
		t.Fatalf("write_mode = %q, want %q", capturedMode, "batched")
	}
}

func TestTransportPut_NoWriteMode_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if capturedMode != "" {
		t.Fatalf("expected no write_mode param, got %q", capturedMode)
	}
}

// ---------------------------------------------------------------------------
// KV Delete – ?write_mode query param
// ---------------------------------------------------------------------------

func TestTransportDelete_WriteModeWALDurable_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodDelete, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.Delete(context.Background(), "k", transport.WithWriteMode(transport.WriteModeWALDurable)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if capturedMode != "wal_durable" {
		t.Fatalf("write_mode = %q, want %q", capturedMode, "wal_durable")
	}
}

func TestTransportDelete_NoWriteMode_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodDelete, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if capturedMode != "" {
		t.Fatalf("expected no write_mode param, got %q", capturedMode)
	}
}

// ---------------------------------------------------------------------------
// KV Get – ?consistency query param
// ---------------------------------------------------------------------------

func TestTransportGet_ConsistencyStrong_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("val"))
	})

	_, err := rt.Get(context.Background(), "k", transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedConsistency != "strong" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "strong")
	}
}

func TestTransportGet_ConsistencyEventual_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("stale"))
	})

	_, err := rt.Get(context.Background(), "k", transport.WithConsistency(transport.ConsistencyEventual))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedConsistency != "eventual" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "eventual")
	}
}

func TestTransportGet_AllowStale_EmitsEventualQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("stale"))
	})

	_, err := rt.Get(context.Background(), "k", transport.WithAllowStale())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedConsistency != "eventual" {
		t.Fatalf("consistency = %q, want %q (AllowStale should map to eventual)", capturedConsistency, "eventual")
	}
}

func TestTransportGet_NoConsistency_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("val"))
	})

	_, err := rt.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedConsistency != "" {
		t.Fatalf("expected no consistency param, got %q", capturedConsistency)
	}
}

// ---------------------------------------------------------------------------
// KV List – ?consistency query param
// ---------------------------------------------------------------------------

func TestTransportList_ConsistencyLinearizable_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, transport.KVListResponse{Keys: []string{"k"}})
	})

	_, err := rt.List(context.Background(), "", transport.WithConsistency(transport.ConsistencyLinearizable))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if capturedConsistency != "linearizable" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "linearizable")
	}
}

func TestTransportList_NoConsistency_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, transport.KVListResponse{Keys: []string{"k"}})
	})

	_, err := rt.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if capturedConsistency != "" {
		t.Fatalf("expected no consistency param, got %q", capturedConsistency)
	}
}

// ---------------------------------------------------------------------------
// KV Scan – ?consistency query param
// ---------------------------------------------------------------------------

func TestTransportScan_ConsistencyStrong_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv/scan"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, transport.KVScanResponse{Pairs: map[string][]byte{"k": []byte("v")}})
	})

	_, err := rt.Scan(context.Background(), "", transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if capturedConsistency != "strong" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "strong")
	}
}

func TestTransportScan_NoConsistency_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv/scan"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, transport.KVScanResponse{Pairs: map[string][]byte{"k": []byte("v")}})
	})

	_, err := rt.Scan(context.Background(), "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if capturedConsistency != "" {
		t.Fatalf("expected no consistency param, got %q", capturedConsistency)
	}
}

// ---------------------------------------------------------------------------
// MVCC PutWithLease – ?write_mode query param
// ---------------------------------------------------------------------------

func TestTransportPutWithLease_WriteModeBatched_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		writeJSON(w, &mvccKVWireResponse{Key: []byte("k"), Value: []byte("v")})
	})

	_, err := rt.PutWithLease(context.Background(), "k", []byte("v"), 1, transport.WithWriteMode(transport.WriteModeBatched))
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if capturedMode != "batched" {
		t.Fatalf("write_mode = %q, want %q", capturedMode, "batched")
	}
}

func TestTransportPutWithLease_NoWriteMode_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedMode string
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedMode = r.URL.Query().Get("write_mode")
		writeJSON(w, &mvccKVWireResponse{Key: []byte("k"), Value: []byte("v")})
	})

	_, err := rt.PutWithLease(context.Background(), "k", []byte("v"), 1)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if capturedMode != "" {
		t.Fatalf("expected no write_mode param, got %q", capturedMode)
	}
}

// ---------------------------------------------------------------------------
// MVCC GetRevision – ?consistency query param
// ---------------------------------------------------------------------------

func TestTransportGetRevision_ConsistencyStrong_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccKVWireResponse{Key: []byte("k"), Value: []byte("rev-v")})
	})

	_, err := rt.GetRevision(context.Background(), "k", transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if capturedConsistency != "strong" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "strong")
	}
}

func TestTransportGetRevision_NoConsistency_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccKVWireResponse{Key: []byte("k"), Value: []byte("rev-v")})
	})

	_, err := rt.GetRevision(context.Background(), "k")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if capturedConsistency != "" {
		t.Fatalf("expected no consistency param, got %q", capturedConsistency)
	}
}

// ---------------------------------------------------------------------------
// MVCC GetAtRevision – ?consistency query param
// ---------------------------------------------------------------------------

func TestTransportGetAtRevision_ConsistencyEventual_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccKVWireResponse{Key: []byte("k"), Value: []byte("atrev-v")})
	})

	_, err := rt.GetAtRevision(context.Background(), "k", 5, transport.WithConsistency(transport.ConsistencyEventual))
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if capturedConsistency != "eventual" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "eventual")
	}
}

func TestTransportGetAtRevision_NoConsistency_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccKVWireResponse{Key: []byte("k"), Value: []byte("atrev-v")})
	})

	_, err := rt.GetAtRevision(context.Background(), "k", 5)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if capturedConsistency != "" {
		t.Fatalf("expected no consistency param, got %q", capturedConsistency)
	}
}

// ---------------------------------------------------------------------------
// MVCC Range – ?consistency query param
// ---------------------------------------------------------------------------

func TestTransportRange_ConsistencyLinearizable_EmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv/range"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccRangeWireResponse{Count: 0})
	})

	_, err := rt.Range(
		context.Background(),
		[]byte("a"), []byte("z"),
		transport.RangeOptions{},
		transport.WithConsistency(transport.ConsistencyLinearizable),
	)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if capturedConsistency != "linearizable" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "linearizable")
	}
}

func TestTransportRange_NoConsistency_OmitsQueryParam(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv/range"), func(w http.ResponseWriter, r *http.Request) {
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccRangeWireResponse{Count: 0})
	})

	_, err := rt.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if capturedConsistency != "" {
		t.Fatalf("expected no consistency param, got %q", capturedConsistency)
	}
}

// ---------------------------------------------------------------------------
// applyReadQueryParams / applyWriteModeQueryParam unit tests
// ---------------------------------------------------------------------------

func TestApplyReadQueryParams_ConsistencySet_AddsParam(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://host/kv/k", nil)
	applyReadQueryParams(req, transport.WithConsistency(transport.ConsistencyStrong))
	q := req.URL.Query()
	if q.Get("consistency") != "strong" {
		t.Fatalf("consistency = %q, want %q", q.Get("consistency"), "strong")
	}
}

func TestApplyReadQueryParams_AllowStale_AddsEventual(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://host/kv/k", nil)
	applyReadQueryParams(req, transport.WithAllowStale())
	q := req.URL.Query()
	if q.Get("consistency") != "eventual" {
		t.Fatalf("consistency = %q, want %q", q.Get("consistency"), "eventual")
	}
}

func TestApplyReadQueryParams_NoneSet_NoParamAdded(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://host/kv/k", nil)
	applyReadQueryParams(req)
	if req.URL.RawQuery != "" {
		t.Fatalf("expected empty query, got %q", req.URL.RawQuery)
	}
}

func TestApplyWriteModeQueryParam_ModeSet_AddsParam(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPut, "http://host/kv/k", nil)
	applyWriteModeQueryParam(req, transport.WithWriteMode(transport.WriteModeWALDurable))
	q := req.URL.Query()
	if q.Get("write_mode") != "wal_durable" {
		t.Fatalf("write_mode = %q, want %q", q.Get("write_mode"), "wal_durable")
	}
}

func TestApplyWriteModeQueryParam_NoneSet_NoParamAdded(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPut, "http://host/kv/k", nil)
	applyWriteModeQueryParam(req)
	if req.URL.RawQuery != "" {
		t.Fatalf("expected empty query, got %q", req.URL.RawQuery)
	}
}

// ---------------------------------------------------------------------------
// Verify existing ?prefix params are preserved when consistency is appended.
// ---------------------------------------------------------------------------

func TestTransportList_PrefixAndConsistency_BothPresent(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedPrefix, capturedConsistency string
	rs.handle(func(r *http.Request) bool {
		return r.Method == http.MethodGet && r.URL.Path == "/kv"
	}, func(w http.ResponseWriter, r *http.Request) {
		capturedPrefix = r.URL.Query().Get("prefix")
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, transport.KVListResponse{Keys: []string{"mypfx/k"}})
	})

	_, err := rt.List(context.Background(), "mypfx/", transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if capturedPrefix != "mypfx/" {
		t.Fatalf("prefix = %q, want %q", capturedPrefix, "mypfx/")
	}
	if capturedConsistency != "strong" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "strong")
	}
}

func TestTransportScan_PrefixAndConsistency_BothPresent(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedPrefix, capturedConsistency string
	rs.handle(func(r *http.Request) bool {
		return r.Method == http.MethodGet && r.URL.Path == "/kv/scan"
	}, func(w http.ResponseWriter, r *http.Request) {
		capturedPrefix = r.URL.Query().Get("prefix")
		capturedConsistency = r.URL.Query().Get("consistency")
		_ = json.NewEncoder(w).Encode(transport.KVScanResponse{
			Pairs: map[string][]byte{capturedPrefix + "k": []byte("v")},
		})
	})

	prefix := "pfx/"
	_, err := rt.Scan(context.Background(), prefix, transport.WithConsistency(transport.ConsistencyEventual))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if capturedPrefix != prefix {
		t.Fatalf("prefix = %q, want %q", capturedPrefix, prefix)
	}
	if capturedConsistency != "eventual" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "eventual")
	}
}

// ---------------------------------------------------------------------------
// Verify existing query params on Range are preserved with consistency added.
// ---------------------------------------------------------------------------

func TestTransportRange_LimitAndConsistency_BothPresent(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedLimit, capturedConsistency string
	rs.handle(exactPath(http.MethodGet, "/kv/range"), func(w http.ResponseWriter, r *http.Request) {
		capturedLimit = r.URL.Query().Get("limit")
		capturedConsistency = r.URL.Query().Get("consistency")
		writeJSON(w, &mvccRangeWireResponse{Count: 0})
	})

	_, err := rt.Range(
		context.Background(),
		[]byte("a"), []byte("z"),
		transport.RangeOptions{Limit: 10},
		transport.WithConsistency(transport.ConsistencyStrong),
	)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if capturedLimit != "10" {
		t.Fatalf("limit = %q, want %q", capturedLimit, "10")
	}
	if capturedConsistency != "strong" {
		t.Fatalf("consistency = %q, want %q", capturedConsistency, "strong")
	}
}

// ---------------------------------------------------------------------------
// Verify no X-QRDB-* headers are set (they were replaced by query params).
// ---------------------------------------------------------------------------

func TestTransportGet_NoXQRDBHeaders(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedHeader string
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-QRDB-Consistency")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("val"))
	})

	_, err := rt.Get(context.Background(), "k", transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedHeader != "" {
		t.Fatalf("expected no X-QRDB-Consistency header, got %q", capturedHeader)
	}
}

func TestTransportPut_NoXQRDBHeaders(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var capturedHeader string
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-QRDB-Write-Mode")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := rt.Put(context.Background(), "k", []byte("v"), transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if capturedHeader != "" {
		t.Fatalf("expected no X-QRDB-Write-Mode header, got %q", capturedHeader)
	}
}

// ---------------------------------------------------------------------------
// Helper: parse URL to check for unknown params during test.
// ---------------------------------------------------------------------------

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic("mustParseURL: " + err.Error())
	}
	return u
}
