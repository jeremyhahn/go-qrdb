// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// A SQL view is pure catalog metadata: a name bound to the text of a SELECT.
// Nothing about it touches the row or index keyspaces, so a view is a single
// replicated catalog record — created with a conditional put, dropped with a
// delete, never journaled, never rewritten. It lives in its own "__view:"
// keyspace rather than under tableCatalogPrefix so that ListTables and the
// table-name resolution path never see view records, and vice versa: the SQL
// engine resolves views through the ViewServicer surface below, after table
// resolution has missed, exactly matching go-mysql-server's own
// table-then-view lookup order.

// viewCatalogPrefix is the key space holding view definitions.
const viewCatalogPrefix = "__view:"

// viewMetadataKey returns the key holding a view's definition.
func viewMetadataKey(database, view string) string {
	return viewCatalogPrefix + database + "/" + view
}

// viewCatalogScanPrefix returns the prefix enumerating a database's views.
func viewCatalogScanPrefix(database string) string {
	return viewCatalogPrefix + database + "/"
}

// ViewDef is a view's persisted definition.
type ViewDef struct {
	// Name is the view's name, unique among the database's views.
	Name string `json:"name"`

	// Database is the owning database.
	Database string `json:"database"`

	// SelectStatement is the text of the defining SELECT, exactly as the
	// engine handed it over. The SQL engine re-parses it on every use, so
	// this is the single source of truth for what the view shows.
	SelectStatement string `json:"select_statement"`

	// CreateStatement is the full CREATE VIEW statement, kept for
	// SHOW CREATE VIEW and information_schema.views.
	CreateStatement string `json:"create_statement"`

	// CreatedAt records when the view was created.
	CreatedAt time.Time `json:"created_at"`
}

// CreateViewRequest contains parameters for creating a view.
type CreateViewRequest struct {
	Database        string `json:"database"`
	Name            string `json:"name"`
	SelectStatement string `json:"select_statement"`
	CreateStatement string `json:"create_statement"`
}

// ViewServicer is the view-catalog surface of the relational catalog. It is
// a separate interface rather than four more methods on TableServicer so
// that existing TableServicer implementations (and their test doubles) keep
// compiling; the concrete *TableService implements both, and consumers that
// need views type-assert for this one.
type ViewServicer interface {
	// CreateView persists a view definition. Returns
	// *ViewAlreadyExistsError when the name is taken.
	CreateView(ctx context.Context, req *CreateViewRequest) (*ViewDef, error)

	// DropView removes a view definition. Returns *ViewNotFoundError when
	// no such view exists.
	DropView(ctx context.Context, database, name string) error

	// GetView returns a view's definition, or *ViewNotFoundError.
	GetView(ctx context.Context, database, name string) (*ViewDef, error)

	// ListViews returns every view definition in the database, in
	// name order.
	ListViews(ctx context.Context, database string) ([]*ViewDef, error)
}

// Compile-time assertion that the table catalog also serves views.
var _ ViewServicer = (*TableService)(nil)

// CreateView persists a view definition with a conditional put, so exactly
// one of two racing CREATE VIEW statements can win.
func (s *TableService) CreateView(ctx context.Context, req *CreateViewRequest) (*ViewDef, error) {
	if err := validateTableRef(req.Database, req.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SelectStatement) == "" {
		return nil, &ViewDefinitionEmptyError{Database: req.Database, Name: req.Name}
	}

	def := &ViewDef{
		Name:            req.Name,
		Database:        req.Database,
		SelectStatement: req.SelectStatement,
		CreateStatement: req.CreateStatement,
		CreatedAt:       time.Now().UTC(),
	}
	data, err := json.Marshal(def)
	if err != nil {
		return nil, &TableMetadataError{Op: "marshal view", Err: err}
	}

	stored, err := s.kv.PutIfAbsent(ctx, viewMetadataKey(req.Database, req.Name), data)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, &ViewAlreadyExistsError{Database: req.Database, Name: req.Name}
	}
	return def, nil
}

// DropView removes a view's definition through the same compare-and-swap
// machinery every other catalog mutation uses: the delete commits only if
// the record's revision is still the one just observed, so a racing
// re-CREATE of the same name is never silently deleted.
func (s *TableService) DropView(ctx context.Context, database, name string) error {
	if err := validateTableRef(database, name); err != nil {
		return err
	}
	key := viewMetadataKey(database, name)
	return s.runCatalogCAS(ctx, database, name, func() ([]Compare, []Op, error) {
		rev, err := s.mvcc.GetRevision(ctx, key)
		if err != nil {
			return nil, nil, &ViewNotFoundError{Database: database, Name: name}
		}
		compares := []Compare{{
			Key: []byte(key), Target: CompareModRevision, Result: CompareEqual, ModRevision: rev.ModRevision,
		}}
		ops := []Op{{Type: OpDelete, Key: []byte(key)}}
		return compares, ops, nil
	})
}

// GetView returns a view's definition.
func (s *TableService) GetView(ctx context.Context, database, name string) (*ViewDef, error) {
	if err := validateTableRef(database, name); err != nil {
		return nil, err
	}
	// The KV surface reports a missing key as an error on some backends and
	// as (nil, nil) on others (compare GetTable, which folds both into
	// not-found the same way): either shape means the view does not exist.
	data, err := s.kv.Get(ctx, viewMetadataKey(database, name))
	if err != nil || len(data) == 0 {
		return nil, &ViewNotFoundError{Database: database, Name: name}
	}
	var def ViewDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, &TableMetadataError{Op: "unmarshal view", Err: err}
	}
	return &def, nil
}

// ListViews returns every view in the database.
func (s *TableService) ListViews(ctx context.Context, database string) ([]*ViewDef, error) {
	if database == "" {
		return nil, &TableEmptyDatabaseError{}
	}
	prefix := viewCatalogScanPrefix(database)
	keys, err := s.kv.List(ctx, prefix)
	if err != nil {
		return nil, &TableMetadataError{Op: "list views", Err: err}
	}

	views := make([]*ViewDef, 0, len(keys))
	for _, key := range keys {
		name := strings.TrimPrefix(key, prefix)
		if name == "" {
			continue
		}
		def, err := s.GetView(ctx, database, name)
		if err != nil {
			// A view deleted between List and Get is not an error; anything
			// else is.
			var notFound *ViewNotFoundError
			if errors.As(err, &notFound) {
				continue
			}
			return nil, err
		}
		views = append(views, def)
	}
	return views, nil
}
