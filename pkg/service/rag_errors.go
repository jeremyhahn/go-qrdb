// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package service

// RAGDocumentNotFoundError is returned when a requested document does not
// exist in the given namespace.
type RAGDocumentNotFoundError struct {
	ID        string
	Namespace string
}

// Error returns a human-readable description of the missing document.
func (e *RAGDocumentNotFoundError) Error() string {
	return "service: rag document not found: " + e.Namespace + "/" + e.ID
}

// RAGIngestError wraps an error that occurred during document ingestion.
type RAGIngestError struct {
	Cause error
}

// Error returns a human-readable description of the ingestion failure.
func (e *RAGIngestError) Error() string {
	return "service: rag ingest failed: " + e.Cause.Error()
}

// Unwrap returns the underlying error.
func (e *RAGIngestError) Unwrap() error {
	return e.Cause
}

// RAGQueryError wraps an error that occurred during a RAG retrieval query.
type RAGQueryError struct {
	Cause error
}

// Error returns a human-readable description of the query failure.
func (e *RAGQueryError) Error() string {
	return "service: rag query failed: " + e.Cause.Error()
}

// Unwrap returns the underlying error.
func (e *RAGQueryError) Unwrap() error {
	return e.Cause
}

// RAGDeleteError wraps an error that occurred during document deletion.
type RAGDeleteError struct {
	ID    string
	Cause error
}

// Error returns a human-readable description of the deletion failure.
func (e *RAGDeleteError) Error() string {
	return "service: rag delete failed for document " + e.ID + ": " + e.Cause.Error()
}

// Unwrap returns the underlying error.
func (e *RAGDeleteError) Unwrap() error {
	return e.Cause
}

// RAGServiceClosedError is returned when an operation is attempted on a
// closed RAGServicer.
type RAGServiceClosedError struct{}

// Error returns a human-readable description.
func (e *RAGServiceClosedError) Error() string {
	return "service: rag service is closed"
}
