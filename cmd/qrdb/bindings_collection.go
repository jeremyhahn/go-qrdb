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
	"strconv"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CollectionBinding provides Wails IPC bindings for collection and
// document management operations.
type CollectionBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewCollectionBinding creates a new CollectionBinding.
func NewCollectionBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *CollectionBinding {
	return &CollectionBinding{logger: log, conn: conn}
}

// Create creates a new collection within a database.
func (b *CollectionBinding) Create(req *transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	b.logger.Debug("Create called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	info, err := cl.Collections().CreateCollection(context.Background(), *req)
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Create completed", "name", info.Name)
	return info, nil
}

// Get retrieves collection information by database and collection name.
func (b *CollectionBinding) Get(database, name string) (*transport.CollectionInfo, error) {
	b.logger.Debug("Get called", "database", database, "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	info, err := cl.Collections().GetCollectionInfo(context.Background(), database, name)
	if err != nil {
		b.logger.Error("Get failed", "database", database, "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("Get completed", "database", database, "name", name)
	return info, nil
}

// List returns all collections within a database.
func (b *CollectionBinding) List(database string) (*transport.ListCollectionsResponse, error) {
	b.logger.Debug("List called", "database", database)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Collections().ListCollections(context.Background(), database)
	if err != nil {
		b.logger.Error("List failed", "database", database, "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "database", database, "count", len(resp.Collections))
	return resp, nil
}

// Drop removes a collection from a database by name.
func (b *CollectionBinding) Drop(database, name string) error {
	b.logger.Debug("Drop called", "database", database, "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Drop failed", "error", err)
		return err
	}
	if err := cl.Collections().DropCollection(context.Background(), database, name); err != nil {
		b.logger.Error("Drop failed", "database", database, "name", name, "error", err)
		return err
	}
	b.logger.Debug("Drop completed", "database", database, "name", name)
	return nil
}

// InsertDocument inserts a document into a collection and returns the
// assigned document ID.
func (b *CollectionBinding) InsertDocument(req *transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	b.logger.Debug("InsertDocument called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("InsertDocument failed", "error", err)
		return nil, err
	}
	resp, err := cl.Collections().InsertDocument(context.Background(), *req)
	if err != nil {
		b.logger.Error("InsertDocument failed", "error", err)
		return nil, err
	}
	b.logger.Debug("InsertDocument completed", "id", resp.ID)
	return resp, nil
}

// GetDocument returns a single document by its ID. The ID is accepted
// as a string to prevent JavaScript precision loss for uint64 values.
func (b *CollectionBinding) GetDocument(database, collection, idStr string) (*transport.DocumentResponse, error) {
	b.logger.Debug("GetDocument called", "database", database, "collection", collection, "id", idStr)
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		b.logger.Error("GetDocument failed: invalid id", "id", idStr, "error", err)
		return nil, err
	}
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetDocument failed", "error", err)
		return nil, err
	}
	doc, err := cl.Collections().GetDocument(context.Background(), database, collection, id)
	if err != nil {
		b.logger.Error("GetDocument failed", "database", database, "collection", collection, "id", id, "error", err)
		return nil, err
	}
	b.logger.Debug("GetDocument completed", "database", database, "collection", collection, "id", id)
	return doc, nil
}

// UpdateDocument replaces an existing document in a collection.
func (b *CollectionBinding) UpdateDocument(req *transport.UpdateDocumentRequest) error {
	b.logger.Debug("UpdateDocument called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateDocument failed", "error", err)
		return err
	}
	if err := cl.Collections().UpdateDocument(context.Background(), *req); err != nil {
		b.logger.Error("UpdateDocument failed", "error", err)
		return err
	}
	b.logger.Debug("UpdateDocument completed")
	return nil
}

// DeleteDocument removes a document from a collection by its ID. The ID
// is accepted as a string to prevent JavaScript precision loss for uint64 values.
func (b *CollectionBinding) DeleteDocument(database, collection, idStr string) error {
	b.logger.Debug("DeleteDocument called", "database", database, "collection", collection, "id", idStr)
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		b.logger.Error("DeleteDocument failed: invalid id", "id", idStr, "error", err)
		return err
	}
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("DeleteDocument failed", "error", err)
		return err
	}
	if err := cl.Collections().DeleteDocument(context.Background(), database, collection, id); err != nil {
		b.logger.Error("DeleteDocument failed", "database", database, "collection", collection, "id", id, "error", err)
		return err
	}
	b.logger.Debug("DeleteDocument completed", "database", database, "collection", collection, "id", id)
	return nil
}

// ListDocuments returns a paginated list of documents in a collection.
func (b *CollectionBinding) ListDocuments(req *transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	b.logger.Debug("ListDocuments called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListDocuments failed", "error", err)
		return nil, err
	}
	resp, err := cl.Collections().ListDocuments(context.Background(), *req)
	if err != nil {
		b.logger.Error("ListDocuments failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ListDocuments completed", "count", len(resp.Documents))
	return resp, nil
}
