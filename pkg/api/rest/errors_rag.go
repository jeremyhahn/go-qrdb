//go:build qrdb_vector

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

package rest

import (
	"errors"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func init() {
	ragErrorMapperFn = mapRAGHTTPError
}

// mapRAGHTTPError maps RAG-specific typed errors to HTTP status codes.
// Returns the status code and true when a mapping is found.
func mapRAGHTTPError(err error) (int, bool) {
	var ragDocNF *service.RAGDocumentNotFoundError
	var ragClosed *service.RAGServiceClosedError

	switch {
	case errors.As(err, &ragDocNF):
		return http.StatusNotFound, true
	case errors.As(err, &ragClosed):
		return http.StatusServiceUnavailable, true
	default:
		return 0, false
	}
}
