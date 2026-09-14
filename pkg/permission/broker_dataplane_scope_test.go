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

import "testing"

// brokerDataPlaneExpectations enumerates every broker data-plane permission
// added in Stage 0.5 together with its expected resource, action, category,
// group, and tenancy scope. It is the single source of truth the table-driven
// tests below assert against.
var brokerDataPlaneExpectations = []struct {
	perm     Permission
	resource string
	action   string
	category string
	group    string
}{
	{ObjStorePut, "objstore", "put", "Object Storage", "Data Services"},
	{ObjStoreGet, "objstore", "get", "Object Storage", "Data Services"},
	{ObjStoreDelete, "objstore", "delete", "Object Storage", "Data Services"},
	{ObjStoreList, "objstore", "list", "Object Storage", "Data Services"},
	{ObjStoreHead, "objstore", "head", "Object Storage", "Data Services"},
	{ObjStoreCopy, "objstore", "copy", "Object Storage", "Data Services"},
	{ObjStoreArchive, "objstore", "archive", "Object Storage", "Data Services"},
	{ObjStoreRestore, "objstore", "restore", "Object Storage", "Data Services"},
	{ObjStoreVerify, "objstore", "verify", "Object Storage", "Data Services"},
	{ObjStoreReplicate, "objstore", "replicate", "Object Storage", "Data Services"},
	{ObjStoreMigrate, "objstore", "migrate", "Object Storage", "Data Services"},
	{ObjStoreBatch, "objstore", "batch", "Object Storage", "Data Services"},
	{ObjStoreReadPolicy, "objstore", "read_policy", "Object Storage", "Data Services"},
	{ObjStoreWritePolicy, "objstore", "write_policy", "Object Storage", "Data Services"},
	{ObjStoreReadBackend, "objstore", "read_backend", "Object Storage", "Data Services"},
	{ObjStoreReadStats, "objstore", "read_stats", "Object Storage", "Data Services"},
	{ObjStoreTenantUsage, "objstore", "tenant_usage", "Object Storage", "Data Services"},
	{ObjStoreTenantQuotaRead, "objstore", "tenant_quota_read", "Object Storage", "Data Services"},
	{ObjStoreTenantQuotaWrite, "objstore", "tenant_quota_write", "Object Storage", "Data Services"},
	{ObjStoreAdmin, "objstore", "admin", "Object Storage", "Data Services"},

	{OrchestratorSpawn, "orchestrator", "spawn", "Orchestrator", "AI Services"},
	{OrchestratorPrompt, "orchestrator", "prompt", "Orchestrator", "AI Services"},
	{OrchestratorStatus, "orchestrator", "status", "Orchestrator", "AI Services"},
	{OrchestratorClose, "orchestrator", "close", "Orchestrator", "AI Services"},
	{OrchestratorList, "orchestrator", "list", "Orchestrator", "AI Services"},
	{OrchestratorTaskCreate, "orchestrator", "task_create", "Orchestrator", "AI Services"},
	{OrchestratorTaskRead, "orchestrator", "task_read", "Orchestrator", "AI Services"},
	{OrchestratorTaskList, "orchestrator", "task_list", "Orchestrator", "AI Services"},
	{OrchestratorTaskClaim, "orchestrator", "task_claim", "Orchestrator", "AI Services"},
	{OrchestratorTaskReadmit, "orchestrator", "task_readmit", "Orchestrator", "AI Services"},
	{OrchestratorTaskBlock, "orchestrator", "task_block", "Orchestrator", "AI Services"},
	{OrchestratorTaskComplete, "orchestrator", "task_complete", "Orchestrator", "AI Services"},
	{OrchestratorTaskDecompose, "orchestrator", "task_decompose", "Orchestrator", "AI Services"},
	{OrchestratorArtifactSubmit, "orchestrator", "artifact_submit", "Orchestrator", "AI Services"},
	{OrchestratorArtifactRead, "orchestrator", "artifact_read", "Orchestrator", "AI Services"},
	{OrchestratorJobStart, "orchestrator", "job_start", "Orchestrator", "AI Services"},
	{OrchestratorJobRead, "orchestrator", "job_read", "Orchestrator", "AI Services"},
	{OrchestratorJobList, "orchestrator", "job_list", "Orchestrator", "AI Services"},
	{OrchestratorJobCancel, "orchestrator", "job_cancel", "Orchestrator", "AI Services"},
	{OrchestratorBackendList, "orchestrator", "backend_list", "Orchestrator", "AI Services"},
	{OrchestratorDiscover, "orchestrator", "discover", "Orchestrator", "AI Services"},
	{OrchestratorHook, "orchestrator", "hook", "Orchestrator", "AI Services"},
	{OrchestratorWorkflowRead, "orchestrator", "workflow_read", "Orchestrator", "AI Services"},
	{OrchestratorWorkflowWrite, "orchestrator", "workflow_write", "Orchestrator", "AI Services"},
	{OrchestratorHealth, "orchestrator", "health", "Orchestrator", "AI Services"},
	{OrchestratorInference, "orchestrator", "inference", "Orchestrator", "AI Services"},
	{OrchestratorAgentRegister, "orchestrator", "agent_register", "Orchestrator", "AI Services"},
	{OrchestratorAgentRead, "orchestrator", "agent_read", "Orchestrator", "AI Services"},
	{OrchestratorAgentList, "orchestrator", "agent_list", "Orchestrator", "AI Services"},
	{OrchestratorAgentRemove, "orchestrator", "agent_remove", "Orchestrator", "AI Services"},
	{OrchestratorAgentHeartbeat, "orchestrator", "agent_heartbeat", "Orchestrator", "AI Services"},
	{OrchestratorCacheRead, "orchestrator", "cache_read", "Orchestrator", "AI Services"},
	{OrchestratorCacheWrite, "orchestrator", "cache_write", "Orchestrator", "AI Services"},
	{OrchestratorBroadcast, "orchestrator", "broadcast", "Orchestrator", "AI Services"},

	{MessagingChannelCreate, "messaging", "channel_create", "Messaging", "Data Services"},
	{MessagingChannelRead, "messaging", "channel_read", "Messaging", "Data Services"},
	{MessagingChannelList, "messaging", "channel_list", "Messaging", "Data Services"},
	{MessagingChannelDelete, "messaging", "channel_delete", "Messaging", "Data Services"},
	{MessagingPublish, "messaging", "publish", "Messaging", "Data Services"},
	{MessagingSubscribe, "messaging", "subscribe", "Messaging", "Data Services"},
	{MessagingUnsubscribe, "messaging", "unsubscribe", "Messaging", "Data Services"},
	{MessagingWatch, "messaging", "watch", "Messaging", "Data Services"},
	{MessagingStats, "messaging", "stats", "Messaging", "Data Services"},

	{SchedulerCreate, "scheduler", "create", "Scheduler", "Data Services"},
	{SchedulerRead, "scheduler", "read", "Scheduler", "Data Services"},
	{SchedulerList, "scheduler", "list", "Scheduler", "Data Services"},
	{SchedulerUpdate, "scheduler", "update", "Scheduler", "Data Services"},
	{SchedulerDelete, "scheduler", "delete", "Scheduler", "Data Services"},
	{SchedulerTrigger, "scheduler", "trigger", "Scheduler", "Data Services"},
	{SchedulerPause, "scheduler", "pause", "Scheduler", "Data Services"},
	{SchedulerResume, "scheduler", "resume", "Scheduler", "Data Services"},
	{SchedulerCancel, "scheduler", "cancel", "Scheduler", "Data Services"},
	{SchedulerHistory, "scheduler", "history", "Scheduler", "Data Services"},
	{SchedulerStatus, "scheduler", "status", "Scheduler", "Data Services"},

	{ParametersRead, "parameters", "read", "Parameters", "Data Services"},
	{ParametersWrite, "parameters", "write", "Parameters", "Data Services"},
	{ParametersDelete, "parameters", "delete", "Parameters", "Data Services"},
	{ParametersList, "parameters", "list", "Parameters", "Data Services"},
	{ParametersSearch, "parameters", "search", "Parameters", "Data Services"},

	{DynamicSecretsRoleCreate, "dynamic_secrets", "role_create", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsRoleRead, "dynamic_secrets", "role_read", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsRoleList, "dynamic_secrets", "role_list", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsRoleDelete, "dynamic_secrets", "role_delete", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsGenerate, "dynamic_secrets", "generate", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsLeaseRenew, "dynamic_secrets", "lease_renew", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsLeaseRevoke, "dynamic_secrets", "lease_revoke", "Dynamic Secrets", "Security Services"},
	{DynamicSecretsLeaseList, "dynamic_secrets", "lease_list", "Dynamic Secrets", "Security Services"},

	{ProjectCreate, "project", "create", "Project", "AI Services"},
	{ProjectRead, "project", "read", "Project", "AI Services"},
	{ProjectList, "project", "list", "Project", "AI Services"},
	{ProjectUpdate, "project", "update", "Project", "AI Services"},
	{ProjectDelete, "project", "delete", "Project", "AI Services"},
	{ProjectGrant, "project", "grant", "Project", "AI Services"},
	{ProjectRevoke, "project", "revoke", "Project", "AI Services"},

	{OllamaHealth, "ollama", "health", "Ollama", "AI Services"},
	{OllamaModelList, "ollama", "model_list", "Ollama", "AI Services"},
	{OllamaModelInfo, "ollama", "model_info", "Ollama", "AI Services"},
	{OllamaEmbed, "ollama", "embed", "Ollama", "AI Services"},
}

// TestBrokerDataPlane_ParseAndRegistered verifies that every broker data-plane
// permission parses cleanly and is present in the global registry.
func TestBrokerDataPlane_ParseAndRegistered(t *testing.T) {
	for _, tc := range brokerDataPlaneExpectations {
		t.Run(string(tc.perm), func(t *testing.T) {
			parsed, err := Parse(string(tc.perm))
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v", tc.perm, err)
			}
			if parsed != tc.perm {
				t.Fatalf("Parse(%q): got %q", tc.perm, parsed)
			}
			if Resource(tc.perm) != tc.resource {
				t.Fatalf("Resource(%q): got %q, want %q", tc.perm, Resource(tc.perm), tc.resource)
			}
			if Action(tc.perm) != tc.action {
				t.Fatalf("Action(%q): got %q, want %q", tc.perm, Action(tc.perm), tc.action)
			}
			if !IsRegistered(tc.perm) {
				t.Fatalf("IsRegistered(%q): got false, want true", tc.perm)
			}
		})
	}
}

// TestBrokerDataPlane_TenantOnlyScope verifies that every broker data-plane
// permission is tagged ScopeTenantOnly in the built-in scope layer.
// nodeStateExceptions are the broker data-plane permissions that describe the
// DAEMON'S OWN state rather than tenant-held material, and are therefore
// declared ScopeSelf instead of ScopeTenantOnly (owner ruling 2026-09-02,
// go-qrdb#183).
//
// Enumerating the EXCEPTIONS rather than adding a scope column to
// brokerDataPlaneExpectations is deliberate and it is the fail-closed
// direction: a permission added to this family and not listed here is asserted
// tenant-only by TestBrokerDataPlane_TenantOnlyScope below. Getting the new
// permission's scope wrong therefore fails a test, whereas a per-row column
// would let a wrong value be typed in and pass.
var nodeStateExceptions = map[Permission]struct{}{
	OrchestratorHealth: {},
}

// Every member of the family except the node-state exceptions is
// ScopeTenantOnly.
//
// Before go-qrdb#183 nothing asserted the scope of any INDIVIDUAL member: the
// whole family was registered in one RegisterScope call and this test looped
// over the same list, so the classification of a single permission could
// change without a failing test -- and two permissions had been recruited into
// tenant-only by exactly that route.
func TestBrokerDataPlane_TenantOnlyScope(t *testing.T) {
	for _, tc := range brokerDataPlaneExpectations {
		if _, skip := nodeStateExceptions[tc.perm]; skip {
			continue
		}
		t.Run(string(tc.perm), func(t *testing.T) {
			if got := mustScope(t, tc.perm); got != ScopeTenantOnly {
				t.Fatalf("LookupScope(%q): got %v, want %v", tc.perm, got, ScopeTenantOnly)
			}
			if IsCrossScope(tc.perm) {
				t.Fatalf("IsCrossScope(%q): got true, want false", tc.perm)
			}
		})
	}
}

// The node-state verbs take ScopeSelf, so the system tenant can read the
// daemon's own orchestrator state.
//
// Under ScopeTenantOnly the denial was STRUCTURAL: the scope check runs after
// the grant check, so no role -- not admin, not owner -- could satisfy it from
// the system tenant, and the operator persona was the single persona that
// could not see whether the node it runs was alive.
func TestBrokerDataPlane_NodeStateScopeIsSelf(t *testing.T) {
	if len(nodeStateExceptions) == 0 {
		t.Fatal("no exceptions listed: this test would pass vacuously")
	}
	for perm := range nodeStateExceptions {
		t.Run(string(perm), func(t *testing.T) {
			if !IsRegistered(perm) {
				t.Fatalf("IsRegistered(%q): got false -- excluding a permission "+
					"from the ScopeTenantOnly set must not unregister it", perm)
			}
			if got := mustScope(t, perm); got != ScopeSelf {
				t.Fatalf("LookupScope(%q): got %v, want ScopeSelf. A tenant-only "+
					"scope denies this to the system tenant structurally "+
					"(go-qrdb#183)", perm, got)
			}
			if IsCrossScope(perm) {
				t.Fatalf("IsCrossScope(%q): got true -- ScopeSelf, not ScopeCross: "+
					"a tenant-local identity must stay confined to its own tenant", perm)
			}
		})
	}
}

// The exception set and the registration function must not drift apart. They
// are two lists of the same thing in two files, which is exactly the shape
// that goes stale.
func TestBrokerDataPlane_NodeStateExceptionsMatchRegistration(t *testing.T) {
	registered := make(map[Permission]struct{}, len(nodeStateScopePermissions()))
	for _, p := range nodeStateScopePermissions() {
		registered[p] = struct{}{}
	}
	for p := range nodeStateExceptions {
		if _, ok := registered[p]; !ok {
			t.Errorf("%q is excluded from the tenant-only assertion but is not in "+
				"nodeStateScopePermissions()", p)
		}
	}
	for p := range registered {
		if _, ok := nodeStateExceptions[p]; !ok {
			t.Errorf("%q is in nodeStateScopePermissions() but is still asserted "+
				"tenant-only", p)
		}
	}
}

// A permission must never be in both sets: brokerDataPlaneScopePermissions()
// is what gets tagged ScopeTenantOnly, so an entry appearing in both would be
// tagged and the exception silently ineffective.
func TestBrokerDataPlane_NodeStateNotAlsoTenantOnly(t *testing.T) {
	for _, p := range brokerDataPlaneScopePermissions() {
		if _, ok := nodeStateExceptions[p]; ok {
			t.Errorf("%q is in BOTH brokerDataPlaneScopePermissions() and the "+
				"node-state set: it will be tagged ScopeTenantOnly and the "+
				"exception has no effect", p)
		}
	}
}

// TestBrokerDataPlane_InAllAndByResource verifies that the broker data-plane
// permissions appear in All() and in ByResource() for their resource.
func TestBrokerDataPlane_InAllAndByResource(t *testing.T) {
	all := make(map[Permission]struct{}, len(All()))
	for _, p := range All() {
		all[p] = struct{}{}
	}
	for _, tc := range brokerDataPlaneExpectations {
		if _, ok := all[tc.perm]; !ok {
			t.Errorf("All() missing %q", tc.perm)
		}
		found := false
		for _, p := range ByResource(tc.resource) {
			if p == tc.perm {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ByResource(%q) missing %q", tc.resource, tc.perm)
		}
	}
}

// TestBrokerDataPlane_InCategories verifies that every broker data-plane
// permission is surfaced under the expected category and logical group.
func TestBrokerDataPlane_InCategories(t *testing.T) {
	type loc struct {
		name  string
		group string
	}
	byPerm := map[Permission]loc{}
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			byPerm[p] = loc{cat.Name, cat.Group}
		}
	}
	for _, tc := range brokerDataPlaneExpectations {
		got, ok := byPerm[tc.perm]
		if !ok {
			t.Errorf("Categories() missing %q", tc.perm)
			continue
		}
		if got.name != tc.category || got.group != tc.group {
			t.Errorf("Categories()[%q]: got (%q,%q), want (%q,%q)",
				tc.perm, got.name, got.group, tc.category, tc.group)
		}
	}
}

// TestBrokerDataPlane_NoDuplicateRegistration verifies that registering the
// broker data-plane permissions again is idempotent and does not inflate the
// registry.
func TestBrokerDataPlane_NoDuplicateRegistration(t *testing.T) {
	before := len(All())
	Register(brokerDataPlaneScopePermissions()...)
	if after := len(All()); after != before {
		t.Fatalf("re-registering broker data-plane perms changed count: before %d, after %d", before, after)
	}
}

// TestBrokerDataPlane_WildcardStillParses guards that whole-segment wildcards on
// the new broker data-plane resources are recognized as wildcards.
func TestBrokerDataPlane_WildcardStillParses(t *testing.T) {
	for _, w := range []Permission{
		"objstore:*", "orchestrator:*", "messaging:*", "scheduler:*",
		"parameters:*", "dynamic_secrets:*", "project:*", "ollama:*",
	} {
		if !IsWildcard(w) {
			t.Errorf("IsWildcard(%q): got false, want true", w)
		}
	}
}
