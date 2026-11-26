package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTableService_ViewLifecycle covers create, get, list and drop of a view
// and the refusals of each.
func TestTableService_ViewLifecycle(t *testing.T) {
	s, store := newTestTableService()
	ctx := context.Background()

	def, err := s.CreateView(ctx, &CreateViewRequest{
		Database: "app", Name: "active", SelectStatement: "SELECT * FROM users",
		CreateStatement: "CREATE VIEW active AS SELECT * FROM users",
	})
	require.NoError(t, err)
	require.Equal(t, "active", def.Name)
	_, err = s.CreateView(ctx, &CreateViewRequest{Database: "app", Name: "recent", SelectStatement: "SELECT 1"})
	require.NoError(t, err)

	_, err = s.CreateView(ctx, &CreateViewRequest{Database: "app", Name: "active", SelectStatement: "SELECT 1"})
	_, ok := errors.AsType[*ViewAlreadyExistsError](err)
	require.True(t, ok, "got %v", err)
	_, err = s.CreateView(ctx, &CreateViewRequest{Database: "app", Name: "blank", SelectStatement: "  "})
	_, ok = errors.AsType[*ViewDefinitionEmptyError](err)
	require.True(t, ok, "got %v", err)
	_, err = s.CreateView(ctx, &CreateViewRequest{Name: "active", SelectStatement: "SELECT 1"})
	require.Error(t, err)

	got, err := s.GetView(ctx, "app", "active")
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM users", got.SelectStatement)
	_, err = s.GetView(ctx, "app", "missing")
	_, ok = errors.AsType[*ViewNotFoundError](err)
	require.True(t, ok, "got %v", err)
	_, err = s.GetView(ctx, "", "active")
	require.Error(t, err)

	views, err := s.ListViews(ctx, "app")
	require.NoError(t, err)
	require.Len(t, views, 2)
	_, err = s.ListViews(ctx, "")
	_, ok = errors.AsType[*TableEmptyDatabaseError](err)
	require.True(t, ok, "got %v", err)

	store.put(viewMetadataKey("app", "broken"), []byte("{"))
	_, err = s.GetView(ctx, "app", "broken")
	_, ok = errors.AsType[*TableMetadataError](err)
	require.True(t, ok, "got %v", err)
	_, err = s.ListViews(ctx, "app")
	require.Error(t, err, "a corrupt view record fails the listing")

	require.NoError(t, s.DropView(ctx, "app", "active"))
	err = s.DropView(ctx, "app", "active")
	_, ok = errors.AsType[*ViewNotFoundError](err)
	require.True(t, ok, "got %v", err)
	require.Error(t, s.DropView(ctx, "", "active"))
}
