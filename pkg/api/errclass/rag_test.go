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

package errclass

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/rag"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestClassify_RAGIngestRefusals pins the classes of an ingest the request
// itself decides: a document over max_document_size is TooLarge (413,
// RESOURCE_EXHAUSTED) and a chunking failure is the caller's input
// (InvalidArgument). Both arrive wrapped in the service's RAGIngestError, so
// the wrapped form is what a transport sees.
func TestClassify_RAGIngestRefusals(t *testing.T) {
	tooLarge := &rag.DocumentTooLargeError{Size: 2048, MaxSize: 1024}
	chunking := &rag.ChunkingError{Cause: errors.New("chunk size must be positive")}
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"document too large", tooLarge, TooLarge},
		{"document too large, wrapped", &service.RAGIngestError{Cause: tooLarge}, TooLarge},
		{"chunking", chunking, InvalidArgument},
		{"chunking, wrapped", &service.RAGIngestError{Cause: chunking}, InvalidArgument},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify(%T) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestClassify_RAGStoreFailureIsNotTheCaller pins that a chunk or vector
// store failure is not reported as the caller's input: it carries no class of
// its own, so an unrecognised cause is a server fault.
func TestClassify_RAGStoreFailureIsNotTheCaller(t *testing.T) {
	err := &service.RAGIngestError{Cause: &rag.StoreError{Op: "store chunk text", Cause: errors.New("disk")}}
	if got := Classify(err); got == InvalidArgument || got == TooLarge {
		t.Errorf("Classify(store failure) = %v, want a server-side class", got)
	}
}
