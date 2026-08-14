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

//go:build !integration

// embedding_coverage_test.go covers the printEmbedResponse helper and the
// embedding models subcommand via a mock embedding client.
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// printEmbedResponse – all output format branches
// ---------------------------------------------------------------------------

func TestPrintEmbedResponse_Table(t *testing.T) {
	var buf bytes.Buffer
	savedWriter := outputWriter
	savedFormat := outputFormat
	outputWriter = &buf
	outputFormat = "table"
	defer func() {
		outputWriter = savedWriter
		outputFormat = savedFormat
	}()

	embeddings := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	if err := printEmbedResponse(embeddings, "text-embed-3", false); err != nil {
		t.Fatalf("printEmbedResponse table: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "text-embed-3") {
		t.Errorf("expected model name in output, got: %q", out)
	}
	if !strings.Contains(out, "2") { // count
		t.Errorf("expected count=2 in output, got: %q", out)
	}
	if !strings.Contains(out, "3") { // dims
		t.Errorf("expected dims=3 in output, got: %q", out)
	}
}

func TestPrintEmbedResponse_Table_Empty(t *testing.T) {
	var buf bytes.Buffer
	savedWriter := outputWriter
	savedFormat := outputFormat
	outputWriter = &buf
	outputFormat = "table"
	defer func() {
		outputWriter = savedWriter
		outputFormat = savedFormat
	}()

	// Empty embeddings → dims=0
	if err := printEmbedResponse(nil, "model-x", true); err != nil {
		t.Fatalf("printEmbedResponse empty: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "model-x") {
		t.Errorf("expected model name in output, got: %q", out)
	}
}

func TestPrintEmbedResponse_JSON(t *testing.T) {
	var buf bytes.Buffer
	savedWriter := outputWriter
	savedFormat := outputFormat
	outputWriter = &buf
	outputFormat = "json"
	defer func() {
		outputWriter = savedWriter
		outputFormat = savedFormat
	}()

	embeddings := [][]float32{{1.0, 2.0}}
	if err := printEmbedResponse(embeddings, "embed-model", false); err != nil {
		t.Fatalf("printEmbedResponse json: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"model"`) {
		t.Errorf("expected 'model' key in JSON, got: %q", out)
	}
}

func TestPrintEmbedResponse_YAML(t *testing.T) {
	var buf bytes.Buffer
	savedWriter := outputWriter
	savedFormat := outputFormat
	outputWriter = &buf

	outputFormat = "yaml"
	defer func() {
		outputWriter = savedWriter
		outputFormat = savedFormat
	}()

	embeddings := [][]float32{{0.5}}
	if err := printEmbedResponse(embeddings, "y-model", true); err != nil {
		t.Fatalf("printEmbedResponse yaml: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "model") {
		t.Errorf("expected 'model' in YAML output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// newEmbeddingModelsCmd – success with mock embedding service
// ---------------------------------------------------------------------------

// mockEmbeddingSvc is a minimal EmbeddingServicer for testing.
type mockEmbeddingSvc struct {
	models []service.ModelInfo
	err    error
}

func (m *mockEmbeddingSvc) Embed(_ context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	embeddings := make([][]float32, len(req.Texts))
	for i := range embeddings {
		embeddings[i] = []float32{0.1, 0.2, 0.3}
	}
	return &service.EmbedResponse{Embeddings: embeddings, Model: "test-model"}, nil
}
func (m *mockEmbeddingSvc) BatchEmbed(_ context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	embeddings := make([][]float32, len(req.Texts))
	for i := range embeddings {
		embeddings[i] = []float32{0.1, 0.2, 0.3}
	}
	return &service.BatchEmbedResponse{Embeddings: embeddings, Model: "test-model"}, nil
}
func (m *mockEmbeddingSvc) ListModels(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &service.ListEmbeddingModelsResponse{Models: m.models}, nil
}
func (m *mockEmbeddingSvc) RegisterProvider(_ string, _ service.EmbeddingProvider) error { return nil }
func (m *mockEmbeddingSvc) DeregisterProvider(_ string) error                            { return nil }
func (m *mockEmbeddingSvc) Close() error                                                 { return nil }

var _ service.EmbeddingServicer = (*mockEmbeddingSvc)(nil)

func TestEmbeddingEmbedCmd_Success(t *testing.T) {
	svc := &mockEmbeddingSvc{}
	tr, err := embedded.New(newMockKVService(), newMockAdminService(),
		embedded.WithEmbeddingService(svc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := sdkClient.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	oldClient := client
	client = sdkClient
	t.Cleanup(func() {
		_ = sdkClient.Close()
		client = oldClient
	})

	cmd := newEmbeddingEmbedCmd()
	_, _ = executeCommand(cmd, "hello world")
}

func TestEmbeddingBatchCmd_Success(t *testing.T) {
	svc := &mockEmbeddingSvc{}
	tr, err := embedded.New(newMockKVService(), newMockAdminService(),
		embedded.WithEmbeddingService(svc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := sdkClient.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	oldClient := client
	client = sdkClient
	t.Cleanup(func() {
		_ = sdkClient.Close()
		client = oldClient
	})

	cmd := newEmbeddingBatchCmd()
	_, _ = executeCommand(cmd, "file1.txt")
}

func TestEmbeddingModelsCmd_Table(t *testing.T) {
	svc := &mockEmbeddingSvc{
		models: []service.ModelInfo{
			{Name: "text-embed-3-small", Dimensions: 1536, MaxTokens: 8191},
			{Name: "text-embed-3-large", Dimensions: 3072, MaxTokens: 8191},
		},
	}
	tr, err := embedded.New(newMockKVService(), newMockAdminService(),
		embedded.WithEmbeddingService(svc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := sdkClient.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	oldClient := client
	client = sdkClient
	t.Cleanup(func() {
		_ = sdkClient.Close()
		client = oldClient
	})

	cmd := newEmbeddingModelsCmd()
	out, err := executeCommand(cmd)
	if err != nil {
		t.Fatalf("embedding models: %v", err)
	}
	if !strings.Contains(out, "text-embed-3-small") {
		t.Errorf("expected model name in output, got: %q", out)
	}
}
