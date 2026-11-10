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

package embedded

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedImportFile_Success(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader("k,v\n1,2"), transport.ImportOptions{Format: "csv"})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("Total = %d", result.Total)
	}
}

func TestEmbeddedImportFile_NilResult(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
}

func TestEmbeddedImportFile_WithErrors(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total:     3,
				Succeeded: 1,
				Failed:    2,
				Errors: []service.ImportError{
					{Row: 2, Key: "k1", Message: "duplicate"},
					{Row: 3, Key: "k2", Message: "invalid"},
				},
			}, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Failed != 2 || len(result.Errors) != 2 {
		t.Fatalf("unexpected: %+v", result)
	}
	if result.Errors[0].Row != 2 || result.Errors[0].Message != "duplicate" {
		t.Fatalf("error[0] = %+v", result.Errors[0])
	}
}

func TestEmbeddedImportFile_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedImportFile_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithBatchService(&fullBatchService{}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedImportFile_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("err = %v, want the service's error", err)
	}
}
