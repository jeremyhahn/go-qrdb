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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandleVectorUpdateNamespace_Success(t *testing.T) {
	var got *service.UpdateNamespaceRequest
	svc := &mockVectorService{
		updateNamespaceFn: func(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
			got = req
			return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: true, Success: true, Timestamp: 5}, nil
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":    "embeddings",
		"dimension":    float64(128),
		"index_config": `{"ef_search":"128"}`,
		"metadata":     `{"owner":"search"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if got.Namespace != "embeddings" || got.Dimension != 128 || got.IndexConfig["ef_search"] != "128" || got.Metadata["owner"] != "search" {
		t.Fatalf("service saw %+v", got)
	}
	var resp service.UpdateNamespaceResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !resp.Success || !resp.RequiresRebuild || resp.Timestamp != 5 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHandleVectorUpdateNamespace_InvalidArguments(t *testing.T) {
	cases := map[string]map[string]any{
		"missing namespace":    {},
		"invalid index config": {"namespace": "embeddings", "index_config": "not-json"},
		"invalid metadata":     {"namespace": "embeddings", "metadata": "not-json"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			handler := findVectorHandler(t, &mockVectorService{}, "qrdb_vector_update_namespace")
			result, err := handler(context.Background(), makeReq(args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected an error result")
			}
		})
	}
}

func TestHandleVectorUpdateNamespace_ServiceError(t *testing.T) {
	refusal := &service.NamespaceNotFoundError{Namespace: "missing"}
	svc := &mockVectorService{
		updateNamespaceFn: func(context.Context, *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
			return nil, refusal
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_namespace")

	_, err := handler(context.Background(), makeReq(map[string]any{"namespace": "missing"}))
	if _, ok := errors.AsType[*ToolError](err); !ok {
		t.Fatalf("err = %T, want *ToolError", err)
	}
	if !errors.Is(err, refusal) {
		t.Fatalf("err = %v, want it to wrap the service refusal", err)
	}
}
