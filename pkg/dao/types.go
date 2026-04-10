// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

// Sort order constants for pagination queries.
const (
	SortAscending  = iota // Sort results in ascending order by key
	SortDescending        // Sort results in descending order by key
)

// PageQuery represents pagination parameters for list operations.
type PageQuery struct {
	// Page is the 1-based page number to retrieve.
	// Page 1 returns the first PageSize entities.
	Page int

	// PageSize is the maximum number of entities to return per page.
	PageSize int

	// SortOrder determines the ordering of results.
	// Use SortAscending or SortDescending.
	SortOrder int

	// SortField specifies a struct field name to sort by.
	// When empty, results are sorted by entity storage key (default).
	SortField string
}

// NewPageQuery creates a PageQuery with sensible defaults.
// Defaults: Page=1, PageSize=25, SortOrder=SortAscending.
func NewPageQuery() PageQuery {
	return PageQuery{
		Page:      1,
		PageSize:  25,
		SortOrder: SortAscending,
	}
}

// PageResult represents a paginated result set.
type PageResult[E any] struct {
	// Entities contains the entities for this page.
	Entities []E `json:"entities"`

	// Page is the current page number (1-based).
	Page int `json:"page"`

	// PageSize is the maximum entities per page.
	PageSize int `json:"page_size"`

	// Total is the total number of entities across all pages.
	Total int `json:"total"`

	// HasMore indicates whether additional pages exist.
	HasMore bool `json:"has_more"`
}

// NewPageResult creates an empty PageResult.
func NewPageResult[E any]() PageResult[E] {
	return PageResult[E]{Entities: make([]E, 0)}
}

// NewPageResultFromQuery creates a PageResult initialized from a PageQuery.
func NewPageResultFromQuery[E any](q PageQuery) PageResult[E] {
	return PageResult[E]{
		Entities: make([]E, 0, q.PageSize),
		Page:     q.Page,
		PageSize: q.PageSize,
	}
}

// PagerFunc is a callback function for processing pages of entities.
// The callback receives the full PageResult including pagination metadata.
// Return nil to continue to the next page, or an error to stop iteration.
type PagerFunc[E any] func(result PageResult[E]) error
