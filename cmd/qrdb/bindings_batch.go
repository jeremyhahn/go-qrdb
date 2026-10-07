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

package main

import (
	"context"
	"os"
	"path/filepath"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// WailsOp is a Wails-friendly representation of a batch operation.
// Wails deserializes JSON into Go structs, and []byte fields trigger
// base64 decoding which fails on plain text strings sent by the
// frontend. This type uses string fields to avoid the issue, and is
// converted to transport.Op at the binding boundary.
type WailsOp struct {
	Type     transport.OpType `json:"type"`
	Key      string           `json:"key"`
	Value    string           `json:"value,omitempty"`
	RangeEnd string           `json:"range_end,omitempty"`
	LeaseID  int64            `json:"lease_id,omitempty"`
}

// toTransportOp converts a WailsOp to the SDK transport.Op type.
func (w *WailsOp) toTransportOp() transport.Op {
	op := transport.Op{
		Type:    w.Type,
		Key:     []byte(w.Key),
		LeaseID: w.LeaseID,
	}
	if w.Value != "" {
		op.Value = []byte(w.Value)
	}
	if w.RangeEnd != "" {
		op.RangeEnd = []byte(w.RangeEnd)
	}
	return op
}

// wailsOpsToTransportOps converts a slice of WailsOp to transport.Op.
func wailsOpsToTransportOps(wops []WailsOp) []transport.Op {
	ops := make([]transport.Op, len(wops))
	for i := range wops {
		ops[i] = wops[i].toTransportOp()
	}
	return ops
}

// FileOpenError is returned when a file cannot be opened for import.
type FileOpenError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *FileOpenError) Error() string {
	return "batch: failed to open file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *FileOpenError) Unwrap() error {
	return e.Err
}

// CollectionImportUnsupportedError is returned when ImportFile is called
// with a non-empty Collection: no transport or server-side path currently
// routes a batch import into a collection (collections only support
// inserting one document at a time via CollectionBinding.InsertDocument),
// so accepting the field and silently importing into the raw KV keyspace
// instead would misroute the caller's data. Reject explicitly rather than
// discard the field.
type CollectionImportUnsupportedError struct {
	Collection string
}

// Error returns a human-readable description.
func (e *CollectionImportUnsupportedError) Error() string {
	return "batch: importing into a collection (\"" + e.Collection + "\") is not supported; " +
		"batch import always writes to the raw KV keyspace -- use CollectionBinding.InsertDocument " +
		"to populate a collection"
}

// WailsImportOptions holds import parameters from the Wails frontend.
// All fields use string JSON tags matching the frontend's camelCase
// serialization.
//
// Collection and Database are accepted (rather than removed) purely so
// ImportFile can detect and reject them with CollectionImportUnsupportedError
// instead of the frontend's values being silently dropped: Database is
// redundant with the connection's already-selected database scope, and
// Collection has no supported server-side import path (see
// CollectionImportUnsupportedError).
type WailsImportOptions struct {
	Format        string `json:"format"`
	KeyField      string `json:"key_field"`
	ValueMode     string `json:"value_mode"`
	ValueField    string `json:"value_field"`
	SkipHeader    bool   `json:"skip_header"`
	Transactional bool   `json:"transactional"`
	Collection    string `json:"collection"`
	Database      string `json:"database"`
}

// BatchBinding exposes atomic batch operations to the Wails frontend.
type BatchBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewBatchBinding creates a new BatchBinding backed by the given
// connection.
func NewBatchBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *BatchBinding {
	return &BatchBinding{logger: log, conn: conn}
}

// Execute applies the given operations atomically. All operations
// succeed or fail together. It accepts WailsOp values with string
// fields to avoid Wails base64 deserialization issues with []byte.
func (b *BatchBinding) Execute(ops []WailsOp) (*transport.BatchResponse, error) {
	b.logger.Debug("Execute called", "opCount", len(ops))
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	resp, err := cl.Batch().ExecuteBatch(context.Background(), wailsOpsToTransportOps(ops))
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Execute completed")
	return resp, nil
}

// ImportFile opens the file at the given path and imports its records
// into the KV store. The opts parameter controls format detection, key
// and value extraction, and transactional behavior. Returns
// *CollectionImportUnsupportedError if opts.Collection is set -- see that
// type's documentation.
func (b *BatchBinding) ImportFile(filePath string, opts WailsImportOptions) (*transport.ImportResult, error) {
	b.logger.Debug("ImportFile called", "path", filePath)

	if opts.Collection != "" {
		b.logger.Error("ImportFile: collection-targeted import requested but unsupported", "collection", opts.Collection)
		return nil, &CollectionImportUnsupportedError{Collection: opts.Collection}
	}

	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ImportFile: load client failed", "error", err)
		return nil, err
	}

	f, err := os.Open(filePath)
	if err != nil {
		b.logger.Error("ImportFile: open file failed", "path", filePath, "error", err)
		return nil, &FileOpenError{Path: filePath, Err: err}
	}
	defer func() { _ = f.Close() }()

	transportOpts := transport.ImportOptions{
		Format:        opts.Format,
		Filename:      filepath.Base(filePath),
		KeyField:      opts.KeyField,
		ValueMode:     opts.ValueMode,
		ValueField:    opts.ValueField,
		SkipHeader:    opts.SkipHeader,
		Transactional: opts.Transactional,
	}

	result, err := cl.Batch().ImportFile(context.Background(), filepath.Base(filePath), f, transportOpts)
	if err != nil {
		b.logger.Error("ImportFile: import failed", "path", filePath, "error", err)
		return nil, err
	}

	b.logger.Debug("ImportFile completed", "total", result.Total, "succeeded", result.Succeeded, "failed", result.Failed)
	return result, nil
}

// AvailableCodecs returns the names of all compiled-in codecs.
func (b *BatchBinding) AvailableCodecs() []string {
	return sdk.AvailableCodecs()
}

// AvailableImportFormats returns the supported import file formats.
func (b *BatchBinding) AvailableImportFormats() []string {
	return []string{"auto", "csv", "json", "ndjson", "yaml", "archive"}
}
