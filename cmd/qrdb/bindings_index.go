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

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// IndexCreateParams carries the parameters for creating a secondary index.
type IndexCreateParams struct {
	Name      string `json:"name"`
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique"`
	Sparse    bool   `json:"sparse"`
	Numeric   bool   `json:"numeric"`
}

// IndexBinding exposes secondary index management to the Wails frontend.
type IndexBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewIndexBinding creates a new IndexBinding backed by the given connection.
func NewIndexBinding(log *logger.Logger, conn *ConnectionBinding) *IndexBinding {
	return &IndexBinding{logger: log, conn: conn}
}

// Create creates a new secondary index with the name, field name, and
// configuration described by params.
func (b *IndexBinding) Create(params IndexCreateParams) error {
	b.logger.Debug("Create called", "name", params.Name, "fieldName", params.FieldName)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return err
	}
	cfg := transport.IndexConfig{
		Unique:  params.Unique,
		Sparse:  params.Sparse,
		Numeric: params.Numeric,
	}
	if err := cl.Index().IndexCreate(context.Background(), params.Name, params.FieldName, cfg); err != nil {
		b.logger.Error("Create failed", "name", params.Name, "error", err)
		return err
	}
	b.logger.Debug("Create completed", "name", params.Name)
	return nil
}

// Drop removes the secondary index with the given name.
func (b *IndexBinding) Drop(name string) error {
	b.logger.Debug("Drop called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Drop failed", "error", err)
		return err
	}
	if err := cl.Index().IndexDrop(context.Background(), name); err != nil {
		b.logger.Error("Drop failed", "name", name, "error", err)
		return err
	}
	b.logger.Debug("Drop completed", "name", name)
	return nil
}

// Query returns the entity keys that match value in the named index.
func (b *IndexBinding) Query(indexName, value string) (*transport.IndexQueryResponse, error) {
	b.logger.Debug("Query called", "indexName", indexName, "value", value)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Query failed", "error", err)
		return nil, err
	}
	resp, err := cl.Index().IndexQuery(context.Background(), indexName, value)
	if err != nil {
		b.logger.Error("Query failed", "indexName", indexName, "error", err)
		return nil, err
	}
	b.logger.Debug("Query completed", "indexName", indexName, "count", len(resp.Keys))
	return resp, nil
}

// Scan returns index entries whose values fall within [start, end).
func (b *IndexBinding) Scan(indexName, start, end string) (*transport.IndexScanResponse, error) {
	b.logger.Debug("Scan called", "indexName", indexName, "start", start, "end", end)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Scan failed", "error", err)
		return nil, err
	}
	// An empty start or end from the UI is an OPEN bound (go-qrdb#36); a
	// non-empty value is an explicit bound.
	var startBound, endBound *string
	if start != "" {
		startBound = &start
	}
	if end != "" {
		endBound = &end
	}
	resp, err := cl.Index().IndexScan(context.Background(), indexName, startBound, endBound)
	if err != nil {
		b.logger.Error("Scan failed", "indexName", indexName, "error", err)
		return nil, err
	}
	b.logger.Debug("Scan completed", "indexName", indexName, "entries", len(resp.Entries))
	return resp, nil
}

// List returns the names of all registered secondary indexes. The prefix
// parameter is accepted for API compatibility but is currently unused.
func (b *IndexBinding) List(_ string) (*transport.IndexListResponse, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Index().IndexList(context.Background())
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "count", len(resp.Names))
	return resp, nil
}
