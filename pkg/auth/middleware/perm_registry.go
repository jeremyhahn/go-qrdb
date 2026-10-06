// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// permRegistry is the shared implementation behind the exact-name
// registration-site permission registries (QUICMethodRegistry,
// MCPToolRegistry). Unlike the REST registry it needs no pattern matcher:
// the native QUIC frame protocol and the MCP tool surface both dispatch on
// an exact name, so a plain map is the whole lookup.
//
// A registry is populated during server construction and is read-only
// afterwards; lookups are safe for concurrent use once registration is done.
type permRegistry struct {
	entries map[string]permission.Permission
}

// register records name -> perm. Registering an empty permission or the same
// name twice panics: both are construction-time bugs (the authorizers are
// DEFAULT-DENY, so a nameless-permission registration would make the
// operation permanently unreachable, and a duplicate indicates two handlers
// fighting over one name), and panicking at construction mirrors the REST
// registry, where chi panics on a duplicate route and the server's startup
// assertion panics on an uncovered one.
func (r *permRegistry) register(transport, name string, perm permission.Permission) {
	if perm == "" {
		panic(transport + ": " + name + " registered without a permission (default-deny would make it unreachable)")
	}
	if _, dup := r.entries[name]; dup {
		panic(transport + ": duplicate permission registration for " + name)
	}
	r.entries[name] = perm
}

// lookup resolves name to its registered permission.
func (r *permRegistry) lookup(name string) (permission.Permission, bool) {
	p, ok := r.entries[name]
	return p, ok
}

// snapshot returns a copy of the registered name -> permission entries, for
// the transport guard tests and startup assertions.
func (r *permRegistry) snapshot() map[string]permission.Permission {
	out := make(map[string]permission.Permission, len(r.entries))
	for name, perm := range r.entries {
		out[name] = perm
	}
	return out
}

// QUICMethodRegistry is the single source of truth for native-QUIC method
// authorization, populated at handler-registration time by the QUIC server's
// registration helpers (see pkg/api/quic buildHandlers). It replaces the old
// hand-maintained quicPermissions table: every frame method declares its
// permission exactly once, at the site where its handler is registered in
// the dispatch map. AuthorizeQUIC consults it with DEFAULT-DENY semantics —
// a method with no entry is denied.
type QUICMethodRegistry struct {
	reg permRegistry
	// public holds the frame methods served WITHOUT authentication
	// (RegisterPublic). It is a curated, built-in set: the only member is
	// invitation redemption, whose caller holds no credential yet.
	public map[string]struct{}
}

// NewQUICMethodRegistry creates an empty QUIC method registry.
func NewQUICMethodRegistry() *QUICMethodRegistry {
	return &QUICMethodRegistry{reg: permRegistry{entries: make(map[string]permission.Permission)}}
}

// Register declares that the named frame method requires perm. It panics on
// an empty permission or a duplicate method (construction-time bugs; see
// permRegistry.register).
func (r *QUICMethodRegistry) Register(method string, perm permission.Permission) {
	if r.IsPublic(method) {
		panic("quic: " + method + " is public and cannot also be permission-mapped")
	}
	r.reg.register("quic", method, perm)
}

// RegisterPublic declares a frame method served WITHOUT authentication or
// authorization: the QUIC server skips both for it, as REST does for a
// RESTRoutePublic route and gRPC for a RegisterPublicGRPCMethods method. A
// method may not be both public and permission-mapped, and may not be
// declared twice; either panics, a construction-time bug like a duplicate.
func (r *QUICMethodRegistry) RegisterPublic(method string) {
	if _, mapped := r.reg.lookup(method); mapped {
		panic("quic: " + method + " is permission-mapped and cannot also be public")
	}
	if _, dup := r.public[method]; dup {
		panic("quic: duplicate public registration for " + method)
	}
	if r.public == nil {
		r.public = make(map[string]struct{})
	}
	r.public[method] = struct{}{}
}

// IsPublic reports whether method was declared with RegisterPublic. A nil
// registry has no public methods.
func (r *QUICMethodRegistry) IsPublic(method string) bool {
	if r == nil {
		return false
	}
	_, ok := r.public[method]
	return ok
}

// Lookup resolves the permission required for a frame method. ok is false
// when the method has no registration (denied by AuthorizeQUIC). A nil
// registry covers nothing, so an unwired registry fails closed.
func (r *QUICMethodRegistry) Lookup(method string) (permission.Permission, bool) {
	if r == nil {
		return "", false
	}
	return r.reg.lookup(method)
}

// Covered reports whether the method carries a registered permission or was
// declared public. The QUIC server's default-deny startup assertion walks the
// dispatch map and fails construction for any handler this registry does not
// cover.
func (r *QUICMethodRegistry) Covered(method string) bool {
	_, ok := r.Lookup(method)
	return ok || r.IsPublic(method)
}

// Entries returns a copy of the method -> permission entries for the
// transport guard tests.
func (r *QUICMethodRegistry) Entries() map[string]permission.Permission {
	if r == nil {
		return nil
	}
	return r.reg.snapshot()
}

// MCPToolRegistry is the single source of truth for MCP tool authorization,
// populated at tool-registration time by the MCP server's addTools helper
// (see pkg/api/mcp). It replaces the old hand-maintained mcpPermissions
// table: every tool declares its permission exactly once, alongside its
// schema and handler at the registration site. AuthorizeMCP consults it with
// DEFAULT-DENY semantics — a tool with no entry is denied.
type MCPToolRegistry struct {
	reg permRegistry
}

// NewMCPToolRegistry creates an empty MCP tool registry.
func NewMCPToolRegistry() *MCPToolRegistry {
	return &MCPToolRegistry{reg: permRegistry{entries: make(map[string]permission.Permission)}}
}

// Register declares that the named tool requires perm. It panics on an empty
// permission or a duplicate tool name (construction-time bugs; see
// permRegistry.register).
func (r *MCPToolRegistry) Register(toolName string, perm permission.Permission) {
	r.reg.register("mcp", toolName, perm)
}

// Lookup resolves the permission required to invoke a tool. ok is false when
// the tool has no registration (denied by AuthorizeMCP). A nil registry
// covers nothing, so an unwired registry fails closed.
func (r *MCPToolRegistry) Lookup(toolName string) (permission.Permission, bool) {
	if r == nil {
		return "", false
	}
	return r.reg.lookup(toolName)
}

// Covered reports whether the tool carries a registered permission.
func (r *MCPToolRegistry) Covered(toolName string) bool {
	_, ok := r.Lookup(toolName)
	return ok
}

// Entries returns a copy of the tool -> permission entries for the transport
// guard tests.
func (r *MCPToolRegistry) Entries() map[string]permission.Permission {
	if r == nil {
		return nil
	}
	return r.reg.snapshot()
}
