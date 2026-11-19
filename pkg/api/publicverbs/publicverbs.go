// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package publicverbs is the one table of the operations every transport
// serves with no login: no client certificate, no credential, no permission.
//
// Each row names one operation on every transport -- REST, gRPC, QUIC, MCP
// and Embedded -- the way sealedwindow.Table names the sealed-window verbs.
// A transport reads its public set from this table and keeps no list of its
// own, so the transports cannot disagree about what a caller with no
// certificate may reach. A capability module's own public methods
// (apimodule.GRPCPublicMethodLister, apimodule.MCPPublicToolLister) are
// declared by the module, not here: this table holds only the built-in
// operations.
//
// Health and invitation redemption are the rows. A load balancer or probe
// asks for health before it holds any credential; an invited user redeems an
// invitation to obtain its first one, the token from InviteUser being the
// authority.
package publicverbs

import (
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// Verb identifies one no-login operation, independent of transport.
type Verb string

const (
	// VerbHealth reports the node's health.
	VerbHealth Verb = "health"
	// VerbInvitationRedeem redeems a one-time invitation token for the
	// invited user's first certificate.
	VerbInvitationRedeem Verb = "invitation-redeem"
)

// Names is one verb's name on every transport.
type Names struct {
	// REST is the method-qualified route, "METHOD /path".
	REST string
	// GRPC is the method tail under the QRDB service -- the suffix after the
	// last "/" in grpc.UnaryServerInfo.FullMethod.
	GRPC string
	// QUIC is the native QUIC method name (e.g. "admin.Health").
	QUIC string
	// MCP is the MCP tool name (e.g. "qrdb_health").
	MCP string
	// Embedded is the in-process embedded transport's method name. The
	// embedded transport runs inside the caller's process and has no
	// certificate to present; the name is here so the table is complete.
	Embedded string
	// RateLimit is the ratelimit.Policy dimension a transport draws on,
	// keyed by the caller's source address, before it serves the verb
	// without authentication (AuthMiddleware.CheckLoginRateLimit). Empty
	// means the verb throttles itself: invitation redemption draws on its
	// own per-source and per-tenant buckets inside the service.
	RateLimit permission.Permission
}

// Verbs lists every row of Table in a fixed order.
var Verbs = []Verb{VerbHealth, VerbInvitationRedeem}

// Table maps each no-login verb to its name on every transport.
var Table = map[Verb]Names{
	VerbHealth: {
		REST:      "GET /health",
		GRPC:      "Health",
		QUIC:      "admin.Health",
		MCP:       "qrdb_health",
		Embedded:  "Health",
		RateLimit: permission.AdminHealth,
	},
	// Redemption is on MCP's separate, unauthenticated invitation endpoint
	// (pkg/api/mcp tools_invitations.go); /mcp admits a caller with no
	// credential to initialize and to the public tools alone.
	VerbInvitationRedeem: {
		REST:     "POST /invitations/redeem",
		GRPC:     "RedeemInvitation",
		QUIC:     "invitation.Redeem",
		MCP:      "qrdb_invitation_redeem",
		Embedded: "RedeemInvitation",
	},
}

// RESTRoute splits v's REST entry into its method and path.
func RESTRoute(v Verb) (method, path string) {
	method, path, _ = strings.Cut(Table[v].REST, " ")
	return method, path
}

// RESTRoutes returns every verb's method-qualified REST route.
func RESTRoutes() []string {
	out := make([]string, 0, len(Verbs))
	for _, v := range Verbs {
		out = append(out, Table[v].REST)
	}
	return out
}

// GRPCFullMethods returns every verb's full gRPC method name under
// serviceName, as grpc.UnaryServerInfo.FullMethod spells it.
func GRPCFullMethods(serviceName string) []string {
	out := make([]string, 0, len(Verbs))
	for _, v := range Verbs {
		out = append(out, "/"+serviceName+"/"+Table[v].GRPC)
	}
	return out
}

// QUICMethods returns every verb's native QUIC method name.
func QUICMethods() map[string]struct{} {
	return names(func(n Names) string { return n.QUIC })
}

// MCPTools returns every verb's MCP tool name.
func MCPTools() map[string]struct{} {
	return names(func(n Names) string { return n.MCP })
}

// EmbeddedMethods returns every verb's embedded transport method name.
func EmbeddedMethods() map[string]struct{} {
	return names(func(n Names) string { return n.Embedded })
}

// QUICVerb resolves a native QUIC method name to its verb.
func QUICVerb(method string) (Verb, bool) {
	for _, v := range Verbs {
		if Table[v].QUIC == method {
			return v, true
		}
	}
	return "", false
}

func names(pick func(Names) string) map[string]struct{} {
	out := make(map[string]struct{}, len(Verbs))
	for _, v := range Verbs {
		out[pick(Table[v])] = struct{}{}
	}
	return out
}
