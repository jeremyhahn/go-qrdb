package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// putRefusingCatalog is a catalog whose every Update fails: a database record
// that cannot be written.
type putRefusingCatalog struct{ database.Catalog }

var _ database.Catalog = putRefusingCatalog{}

func (putRefusingCatalog) Update(context.Context, string, string, string, func(*database.DatabaseMeta) error) error {
	return catalogPutRefusedError{}
}

// catalogPutRefusedError is putRefusingCatalog's refusal.
type catalogPutRefusedError struct{}

func (catalogPutRefusedError) Error() string { return "catalog put refused" }
