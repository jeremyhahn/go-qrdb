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
	reg     permRegistry
	targets map[string]QUICTargetFunc
}

// QUICTargetFunc extracts the Target a QUIC frame's payload names. A payload
// it cannot read names the caller's own tenant; the handler then rejects the
// malformed payload itself.
type QUICTargetFunc func(payload []byte) Target

// SetTarget declares how method's payload names the tenant it acts on, so
// AuthorizeQUIC authorizes the method's permission against that tenant (see
// Target). The method must already be registered: attaching a target to an
// unregistered method panics, a construction-time bug like a duplicate.
func (r *QUICMethodRegistry) SetTarget(method string, fn QUICTargetFunc) {
	if _, ok := r.reg.lookup(method); !ok {
		panic("quic: SetTarget on unregistered method " + method)
	}
	if r.targets == nil {
		r.targets = make(map[string]QUICTargetFunc)
	}
	r.targets[method] = fn
}

// target returns the Target payload names under method's extractor, or the
// caller's own tenant when the method declares none.
func (r *QUICMethodRegistry) target(method string, payload []byte) Target {
	if r == nil {
		return SelfTarget()
	}
	fn, ok := r.targets[method]
	if !ok {
		return SelfTarget()
	}
	return fn(payload)
}

// NewQUICMethodRegistry creates an empty QUIC method registry.
func NewQUICMethodRegistry() *QUICMethodRegistry {
	return &QUICMethodRegistry{reg: permRegistry{entries: make(map[string]permission.Permission)}}
}

// Register declares that the named frame method requires perm. It panics on
// an empty permission or a duplicate method (construction-time bugs; see
// permRegistry.register).
func (r *QUICMethodRegistry) Register(method string, perm permission.Permission) {
	r.reg.register("quic", method, perm)
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

// Covered reports whether the method carries a registered permission. The
// QUIC server's default-deny startup assertion walks the dispatch map and
// fails construction for any handler this registry does not cover.
func (r *QUICMethodRegistry) Covered(method string) bool {
	_, ok := r.Lookup(method)
	return ok
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
	reg     permRegistry
	targets map[string]MCPTargetFunc
}

// MCPTargetFunc extracts the Target an MCP tool call's arguments name. An
// argument it cannot read names the caller's own tenant; the tool then
// rejects the malformed argument itself.
type MCPTargetFunc func(args map[string]any) Target

// SetTarget declares how toolName's arguments name the tenant it acts on, so
// AuthorizeMCP authorizes the tool's permission against that tenant (see
// Target). The tool must already be registered; attaching a target to an
// unregistered tool panics.
func (r *MCPToolRegistry) SetTarget(toolName string, fn MCPTargetFunc) {
	if _, ok := r.reg.lookup(toolName); !ok {
		panic("mcp: SetTarget on unregistered tool " + toolName)
	}
	if r.targets == nil {
		r.targets = make(map[string]MCPTargetFunc)
	}
	r.targets[toolName] = fn
}

// target returns the Target args name under toolName's extractor, or the
// caller's own tenant when the tool declares none.
func (r *MCPToolRegistry) target(toolName string, args map[string]any) Target {
	if r == nil {
		return SelfTarget()
	}
	fn, ok := r.targets[toolName]
	if !ok {
		return SelfTarget()
	}
	return fn(args)
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
