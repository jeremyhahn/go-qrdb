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

// Package dao provides a generic Data Access Object layer for DragonDB.
// It offers type-safe CRUD operations with pagination support, working
// seamlessly with all DragonDB storage backends (Raft, PebbleDB, Memory).
package dao

// Entity is the base interface for all persistable entities.
// Any struct that needs to be stored via GenericDAO must implement this interface.
//
// The EntityID is a unique identifier within an entity type namespace.
// If EntityID() returns 0 during Save(), a new ID is auto-generated
// using the current Unix nanosecond timestamp.
//
// Example implementation:
//
//	type User struct {
//	    ID    uint64 `json:"id"`
//	    Name  string `json:"name"`
//	    Email string `json:"email"`
//	}
//
//	func (u *User) EntityID() uint64      { return u.ID }
//	func (u *User) SetEntityID(id uint64) { u.ID = id }
type Entity interface {
	// EntityID returns the unique identifier for this entity.
	// Returns 0 if the entity has not been persisted yet.
	EntityID() uint64

	// SetEntityID sets the entity's unique identifier.
	// Called automatically by Save() when EntityID() returns 0.
	SetEntityID(id uint64)
}
