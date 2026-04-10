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

// TestEntity is a test entity that implements the Entity interface.
// This type is defined without build tags so it can be used by all tests.
type TestEntity struct {
	ID    uint64 `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (e *TestEntity) EntityID() uint64 {
	return e.ID
}

func (e *TestEntity) SetEntityID(id uint64) {
	e.ID = id
}
