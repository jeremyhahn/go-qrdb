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

package provider

import (
	"errors"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Database implements sql.ViewDatabase over the relational catalog's view
// records (see pkg/service/view_service.go). go-mysql-server's analyzer
// resolves a name it could not find as a table through this interface, parses
// the stored SELECT, and plans the query against it — so a view behaves as a
// live subquery over current data, exactly PostgreSQL's and MySQL's model.
//
// Views ride the same per-session tenant resolution as every relational
// table: the ViewServicer is resolved from the registry on each call, so a
// view created by one tenant is invisible to every other.
//
// The read-only ad-hoc SQL surface cannot reach any of this: readOnlyDatabase
// wraps the concrete type behind the bare sql.Database interface, so the
// analyzer's ViewDatabase assertion fails there and views are neither
// creatable, droppable, NOR resolvable on that surface. That last part is
// deliberate fail-closed consistency with how that wrapper strips every
// other capability interface — see readonly_database.go.
var _ sqle.ViewDatabase = (*Database)(nil)

// viewService resolves the session tenant's view catalog. A database built
// without a registry (the KV-only embedded shape) has no relational catalog
// and therefore no views.
func (d *Database) viewService(ctx *sqle.Context) (service.ViewServicer, error) {
	if d.registry == nil {
		return nil, sqle.ErrViewsNotSupported.New(d.name)
	}
	tableSvc, err := d.registry.TableService(d.sessionTenantID(ctx), d.name)
	if err != nil {
		return nil, err
	}
	viewSvc, ok := tableSvc.(service.ViewServicer)
	if !ok {
		return nil, sqle.ErrViewsNotSupported.New(d.name)
	}
	return viewSvc, nil
}

// CreateView persists a view definition. The name must not collide with an
// existing view; go-mysql-server has already checked tables by the time it
// calls this.
func (d *Database) CreateView(ctx *sqle.Context, name string, selectStatement, createViewStmt string) error {
	viewSvc, err := d.viewService(ctx)
	if err != nil {
		return err
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	_, err = viewSvc.CreateView(ctx, &service.CreateViewRequest{
		Database:        d.name,
		Name:            name,
		SelectStatement: selectStatement,
		CreateStatement: createViewStmt,
	})
	var exists *service.ViewAlreadyExistsError
	if errors.As(err, &exists) {
		// The analyzer branches on this exact kind for
		// CREATE OR REPLACE / IF NOT EXISTS handling.
		return sqle.ErrExistingView.New(d.name, name)
	}
	return err
}

// DropView removes a view definition.
func (d *Database) DropView(ctx *sqle.Context, name string) error {
	viewSvc, err := d.viewService(ctx)
	if err != nil {
		return err
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	err = viewSvc.DropView(ctx, d.name, name)
	var notFound *service.ViewNotFoundError
	if errors.As(err, &notFound) {
		// The analyzer branches on this kind for DROP VIEW IF EXISTS.
		return sqle.ErrViewDoesNotExist.New(d.name, name)
	}
	return err
}

// GetViewDefinition returns a view's definition, or false when the name is
// not a view. Every error other than "no such view" propagates: reporting a
// storage failure as "not a view" would turn an outage into a wrong
// "table not found" for every query that touches the view.
func (d *Database) GetViewDefinition(ctx *sqle.Context, viewName string) (sqle.ViewDefinition, bool, error) {
	if d.registry == nil {
		// A database without a relational catalog HAS no views; this is the
		// "none exists" answer, not an error, so plain table resolution on
		// the embedded KV-only shape keeps working untouched.
		return sqle.ViewDefinition{}, false, nil
	}
	// A registry that cannot resolve a relational catalog for this database
	// (KV-only shapes, narrow test registries) has no views to serve. That
	// is the "none exists" answer, not an error: this method runs on EVERY
	// table-resolution miss, and erroring here would break plain table
	// lookups on databases that never had a view.
	viewSvc, err := d.viewService(ctx)
	if err != nil {
		return sqle.ViewDefinition{}, false, nil
	}
	def, err := viewSvc.GetView(ctx, d.name, viewName)
	if err != nil {
		var notFound *service.ViewNotFoundError
		if errors.As(err, &notFound) {
			return sqle.ViewDefinition{}, false, nil
		}
		return sqle.ViewDefinition{}, false, err
	}
	return toSQLViewDefinition(def), true, nil
}

// AllViews returns every view in the database.
func (d *Database) AllViews(ctx *sqle.Context) ([]sqle.ViewDefinition, error) {
	if d.registry == nil {
		return nil, nil
	}
	viewSvc, err := d.viewService(ctx)
	if err != nil {
		// Same reasoning as GetViewDefinition: no catalog means no views.
		return nil, nil
	}
	defs, err := viewSvc.ListViews(ctx, d.name)
	if err != nil {
		return nil, err
	}
	out := make([]sqle.ViewDefinition, len(defs))
	for i, def := range defs {
		out[i] = toSQLViewDefinition(def)
	}
	return out, nil
}

// toSQLViewDefinition converts a catalog record to go-mysql-server's shape.
// SqlMode is left empty: qrdb pins one SQL mode process-wide rather than
// persisting a per-view snapshot of it.
func toSQLViewDefinition(def *service.ViewDef) sqle.ViewDefinition {
	return sqle.ViewDefinition{
		Name:                def.Name,
		TextDefinition:      def.SelectStatement,
		CreateViewStatement: def.CreateStatement,
	}
}

// userSequencer resolves the session tenant's client-named-sequence
// surface, mirroring viewService above.
func (d *Database) userSequencer(ctx *sqle.Context) (service.UserSequencer, error) {
	if d.registry == nil {
		return nil, &SequencesUnavailableError{Database: d.name}
	}
	tableSvc, err := d.registry.TableService(d.sessionTenantID(ctx), d.name)
	if err != nil {
		return nil, err
	}
	seq, ok := tableSvc.(service.UserSequencer)
	if !ok {
		return nil, &SequencesUnavailableError{Database: d.name}
	}
	return seq, nil
}

// NextSequenceValue allocates the next value of the named client sequence.
// It is the storage half of the SQL nextval() function pkg/sql/engine
// registers for the PostgreSQL dialect.
func (d *Database) NextSequenceValue(ctx *sqle.Context, name string) (int64, error) {
	seq, err := d.userSequencer(ctx)
	if err != nil {
		return 0, err
	}
	value, err := seq.NextUserSequenceValue(ctx, d.name, name)
	if err != nil {
		return 0, err
	}
	return int64(value), nil
}

// ResetSequence returns the named client sequence to its zero state --
// DROP SEQUENCE's effect under materialize-on-first-use semantics (see
// service.UserSequencer).
func (d *Database) ResetSequence(ctx *sqle.Context, name string) error {
	seq, err := d.userSequencer(ctx)
	if err != nil {
		return err
	}
	return seq.ResetUserSequence(ctx, d.name, name)
}
