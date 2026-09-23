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

package permission

// ---------------------------------------------------------------------------
// Broker-domain permissions
//
// These canonical resource:action constants extend qrdb's registry so it can
// serve as the single RBAC policy decision point (PDP) for the whole stack.
// They cover capabilities that live above qrdb's own engine (the QuicBaaS
// broker / xkms trust + ACME surfaces, the MCP transport, and the agent
// runtime) but that the stack enforces through qrdb's canonical vocabulary.
//
// Reconciliation rule: qrdb OWNS the resource names. Where a consumer used a
// different name for a capability qrdb already exposes (e.g. broker
// kms/certificate/credential vs qrdb keys/certs/credentials, or broker
// crypto:* vs qrdb keys:*), the consumer's table is rewritten to qrdb's name;
// no synonym is registered here. This file adds ONLY genuinely broker-domain
// resources that qrdb previously lacked:
//
//   - secrets        tenant secret-material vault (distinct from passwords /
//                    named credentials)
//   - trust          cross-sign / AIA / DANE trust management
//   - acme           RFC 8555 ACME issuance management
//   - mcp            Model Context Protocol transport scopes
//   - agent:runtime  agent runtime lifecycle scopes
//
// trust, acme, MCP sessions, and agent runtimes are tagged ScopeTenantOnly at
// init time: they are tenant-owned surfaces held ON A TENANT'S BEHALF, so a
// platform (system) operator must not exercise them, and they are never
// cross-tenant. (The RBAC ScopeTenantOnly rule admits them only when the
// identity is NOT in the system tenant AND the identity's tenant matches the
// request's target tenant.)
//
// secrets:* is the one exception, tagged ScopeSelfStrict instead. It is a
// per-tenant vault, and the system tenant needs a private vault over its OWN
// namespace exactly as every other tenant does -- ScopeTenantOnly would deny
// it that outright, which is a regression relative to the pre-broker world
// where the system tenant reached its own secret material through the
// cross-scoped credentials:store. ScopeSelfStrict admits every identity,
// system tenant included, against its own tenant only -- no cross-tenant
// lift, no system-tenant denial (go-qrdb#372).
// ---------------------------------------------------------------------------

const (
	// SecretsRead grants read access to tenant secret material.
	SecretsRead Permission = "secrets:read"
	// SecretsWrite grants write access to tenant secret material.
	SecretsWrite Permission = "secrets:write"
	// SecretsDelete grants delete access to tenant secret material.
	SecretsDelete Permission = "secrets:delete"
	// SecretsList grants list access to tenant secret names.
	SecretsList Permission = "secrets:list"
)

const (
	// TrustRead grants read access to trust artifacts (cross-signed pools,
	// AIA chains, DANE TLSA records).
	TrustRead Permission = "trust:read"
	// TrustWrite grants write access to trust artifacts (cross-sign, publish
	// AIA, manage DANE TLSA records).
	TrustWrite Permission = "trust:write"
)

const (
	// ACMERead grants read access to ACME account and order state.
	ACMERead Permission = "acme:read"
	// ACMEWrite grants write access to ACME accounts, orders, and issuance.
	ACMEWrite Permission = "acme:write"
)

const (
	// MCPTools grants access to invoke MCP tools over the MCP transport.
	MCPTools Permission = "mcp:tools"
	// MCPResources grants access to read MCP resources over the MCP transport.
	MCPResources Permission = "mcp:resources"
	// MCPAdmin grants administrative control over the MCP transport surface.
	MCPAdmin Permission = "mcp:admin"
)

const (
	// AgentRuntimeSpawn grants access to spawn an agent runtime.
	AgentRuntimeSpawn Permission = "agent:runtime:spawn"
	// AgentRuntimePrompt grants access to send a prompt to an agent runtime.
	AgentRuntimePrompt Permission = "agent:runtime:prompt"
	// AgentRuntimeStatus grants access to query agent runtime status.
	AgentRuntimeStatus Permission = "agent:runtime:status"
)

// brokerScopePermissions returns every broker-domain permission declared in
// this file as a single slice, for Register() and Categories(). The init()
// function below tags each member's SCOPE separately -- secrets:* is
// ScopeSelfStrict, everything else here is ScopeTenantOnly -- by naming the
// members of each group explicitly rather than through a shared helper (a
// family tagged through a helper silently picks up whatever the helper later
// grows to cover).
func brokerScopePermissions() []Permission {
	return []Permission{
		SecretsRead, SecretsWrite, SecretsDelete, SecretsList,
		TrustRead, TrustWrite,
		ACMERead, ACMEWrite,
		MCPTools, MCPResources, MCPAdmin,
		AgentRuntimeSpawn, AgentRuntimePrompt, AgentRuntimeStatus,
	}
}

// brokerCategories groups the broker-domain permissions for UI rendering.
// They are appended to the authoritative categories slice in init() so they
// surface in Categories() alongside qrdb's engine permissions.
func brokerCategories() []Category {
	return []Category{
		{
			Name:  "Secrets",
			Group: "Security Services",
			Permissions: []Permission{
				SecretsRead, SecretsWrite, SecretsDelete, SecretsList,
			},
		},
		{
			Name:  "Trust",
			Group: "Security Services",
			Permissions: []Permission{
				TrustRead, TrustWrite,
			},
		},
		{
			Name:  "ACME",
			Group: "Security Services",
			Permissions: []Permission{
				ACMERead, ACMEWrite,
			},
		},
		{
			Name:  "MCP Transport",
			Group: "Transport Services",
			Permissions: []Permission{
				MCPTools, MCPResources, MCPAdmin,
			},
		},
		{
			Name:  "Agent Runtime",
			Group: "AI Services",
			Permissions: []Permission{
				AgentRuntimeSpawn, AgentRuntimePrompt, AgentRuntimeStatus,
			},
		},
	}
}

func init() {
	perms := brokerScopePermissions()
	Register(perms...)
	RegisterScope(ScopeSelfStrict, SecretsRead, SecretsWrite, SecretsDelete, SecretsList)
	RegisterScope(ScopeTenantOnly,
		TrustRead, TrustWrite,
		ACMERead, ACMEWrite,
		MCPTools, MCPResources, MCPAdmin,
		AgentRuntimeSpawn, AgentRuntimePrompt, AgentRuntimeStatus,
	)
	categories = append(categories, brokerCategories()...)
}
