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
// Broker data-plane permissions (Stage 0.5)
//
// These canonical resource:action constants extend qrdb's registry so it can
// serve as the single RBAC policy decision point (PDP) for the QuicBaaS broker
// data-plane surfaces that qrdb's engine does not implement itself but that the
// stack still enforces through qrdb's canonical vocabulary.
//
// Before Stage 0.5 these surfaces had NO qrdb permission family, so the broker
// PEP returned ok=false and they were blanket-denied the moment RequireAuthz
// flipped on. This file registers a real family per surface so the PEP can
// resolve each operation to a registered permission instead.
//
// Surfaces covered (each grounded in the broker's actual REST routes, gRPC
// methods, QUIC opcodes, and MCP tools):
//
//   - objstore         tenant object storage (put/get/list/admin/...)
//   - orchestrator     agent orchestration sessions/tasks/jobs/agents
//   - messaging        pub/sub channels + delivery
//   - scheduler        cron/at job scheduling
//   - parameters       hierarchical parameter store
//   - dynamic_secrets  on-demand leased credential generation
//   - project          agent project + membership management
//   - ollama           local embedding model inference
//
// Every permission declared here is tagged ScopeTenantOnly at init time: these
// are tenant-owned data-plane surfaces. A platform (system) operator must not
// exercise them on a tenant's behalf, and they are never cross-tenant. (The
// RBAC ScopeTenantOnly rule admits them only when the identity is NOT in the
// system tenant AND the identity's tenant matches the request's target tenant.)
//
// No built-in role is granted these permissions here. Candidate grants are
// reported separately for explicit operator review; auto-granting is out of
// scope for this additive registration.
// ---------------------------------------------------------------------------

const (
	// ObjStorePut grants write access to store a tenant object.
	ObjStorePut Permission = "objstore:put"
	// ObjStoreGet grants read access to retrieve a tenant object.
	ObjStoreGet Permission = "objstore:get"
	// ObjStoreDelete grants delete access to a tenant object.
	ObjStoreDelete Permission = "objstore:delete"
	// ObjStoreList grants list access over tenant objects.
	ObjStoreList Permission = "objstore:list"
	// ObjStoreHead grants metadata-only read access to a tenant object.
	ObjStoreHead Permission = "objstore:head"
	// ObjStoreCopy grants access to copy a tenant object.
	ObjStoreCopy Permission = "objstore:copy"
	// ObjStoreArchive grants access to archive a tenant object to cold storage.
	ObjStoreArchive Permission = "objstore:archive"
	// ObjStoreRestore grants access to restore an archived tenant object.
	ObjStoreRestore Permission = "objstore:restore"
	// ObjStoreVerify grants access to verify a tenant object's integrity.
	ObjStoreVerify Permission = "objstore:verify"
	// ObjStoreReplicate grants access to replicate a tenant object across backends.
	ObjStoreReplicate Permission = "objstore:replicate"
	// ObjStoreMigrate grants access to migrate a tenant object between backends.
	ObjStoreMigrate Permission = "objstore:migrate"
	// ObjStoreBatch grants access to batch object put/get/delete operations.
	ObjStoreBatch Permission = "objstore:batch"
	// ObjStoreReadPolicy grants read access to an object's storage policy.
	ObjStoreReadPolicy Permission = "objstore:read_policy"
	// ObjStoreWritePolicy grants write access to an object's storage policy.
	ObjStoreWritePolicy Permission = "objstore:write_policy"
	// ObjStoreReadBackend grants read access to object storage backend listing
	// and health.
	ObjStoreReadBackend Permission = "objstore:read_backend"
	// ObjStoreReadStats grants read access to object storage statistics.
	ObjStoreReadStats Permission = "objstore:read_stats"
	// ObjStoreTenantUsage grants read access to a tenant's object storage usage.
	ObjStoreTenantUsage Permission = "objstore:tenant_usage"
	// ObjStoreTenantQuotaRead grants read access to a tenant's storage quotas.
	ObjStoreTenantQuotaRead Permission = "objstore:tenant_quota_read"
	// ObjStoreTenantQuotaWrite grants write access to a tenant's storage quotas.
	ObjStoreTenantQuotaWrite Permission = "objstore:tenant_quota_write"
	// ObjStoreAdmin grants administrative control over object storage backends
	// (create/update/delete/status, GC, repair, compaction).
	ObjStoreAdmin Permission = "objstore:admin"
)

const (
	// OrchestratorSpawn grants access to spawn an orchestration session.
	OrchestratorSpawn Permission = "orchestrator:spawn"
	// OrchestratorPrompt grants access to prompt an orchestration session.
	OrchestratorPrompt Permission = "orchestrator:prompt"
	// OrchestratorStatus grants read access to orchestration session status.
	OrchestratorStatus Permission = "orchestrator:status"
	// OrchestratorClose grants access to close an orchestration session.
	OrchestratorClose Permission = "orchestrator:close"
	// OrchestratorList grants list access over orchestration sessions.
	OrchestratorList Permission = "orchestrator:list"
	// OrchestratorTaskCreate grants access to create an orchestration task.
	OrchestratorTaskCreate Permission = "orchestrator:task_create"
	// OrchestratorTaskRead grants read access to an orchestration task.
	OrchestratorTaskRead Permission = "orchestrator:task_read"
	// OrchestratorTaskList grants list access over orchestration tasks.
	OrchestratorTaskList Permission = "orchestrator:task_list"
	// OrchestratorTaskClaim grants access to claim an orchestration task.
	OrchestratorTaskClaim Permission = "orchestrator:task_claim"
	// OrchestratorTaskComplete grants access to complete an orchestration task.
	OrchestratorTaskComplete Permission = "orchestrator:task_complete"
	// OrchestratorTaskDecompose grants access to decompose an orchestration task.
	OrchestratorTaskDecompose Permission = "orchestrator:task_decompose"
	// OrchestratorArtifactSubmit grants access to submit task artifacts.
	OrchestratorArtifactSubmit Permission = "orchestrator:artifact_submit"
	// OrchestratorArtifactRead grants read access to task artifacts.
	OrchestratorArtifactRead Permission = "orchestrator:artifact_read"
	// OrchestratorJobStart grants access to start an orchestration job.
	OrchestratorJobStart Permission = "orchestrator:job_start"
	// OrchestratorJobRead grants read access to an orchestration job.
	OrchestratorJobRead Permission = "orchestrator:job_read"
	// OrchestratorJobList grants list access over orchestration jobs.
	OrchestratorJobList Permission = "orchestrator:job_list"
	// OrchestratorJobCancel grants access to cancel an orchestration job.
	OrchestratorJobCancel Permission = "orchestrator:job_cancel"
	// OrchestratorBackendList grants list access over orchestration backends.
	OrchestratorBackendList Permission = "orchestrator:backend_list"
	// OrchestratorDiscover grants access to discover orchestration capabilities.
	OrchestratorDiscover Permission = "orchestrator:discover"
	// OrchestratorHook grants access to deliver orchestration hook events.
	OrchestratorHook Permission = "orchestrator:hook"
	// OrchestratorWorkflowRead grants read/list access to orchestration workflows.
	OrchestratorWorkflowRead Permission = "orchestrator:workflow_read"
	// OrchestratorWorkflowWrite grants write/delete access to workflows.
	OrchestratorWorkflowWrite Permission = "orchestrator:workflow_write"
	// OrchestratorHealth grants read access to orchestration health.
	OrchestratorHealth Permission = "orchestrator:health"
	// OrchestratorInference grants access to run orchestration inference.
	OrchestratorInference Permission = "orchestrator:inference"
	// OrchestratorAgentRegister grants access to register an orchestration agent.
	OrchestratorAgentRegister Permission = "orchestrator:agent_register"
	// OrchestratorAgentRead grants read access to an orchestration agent.
	OrchestratorAgentRead Permission = "orchestrator:agent_read"
	// OrchestratorAgentList grants list access over orchestration agents.
	OrchestratorAgentList Permission = "orchestrator:agent_list"
	// OrchestratorAgentRemove grants access to remove an orchestration agent.
	OrchestratorAgentRemove Permission = "orchestrator:agent_remove"
	// OrchestratorAgentHeartbeat grants access to send an agent heartbeat.
	OrchestratorAgentHeartbeat Permission = "orchestrator:agent_heartbeat"
	// OrchestratorCacheRead grants read access to the orchestration shared cache.
	OrchestratorCacheRead Permission = "orchestrator:cache_read"
	// OrchestratorCacheWrite grants write access to the orchestration shared cache.
	OrchestratorCacheWrite Permission = "orchestrator:cache_write"
	// OrchestratorBroadcast grants access to broadcast to orchestration agents.
	OrchestratorBroadcast Permission = "orchestrator:broadcast"
)

const (
	// MessagingChannelCreate grants access to create a messaging channel.
	MessagingChannelCreate Permission = "messaging:channel_create"
	// MessagingChannelRead grants read access to a messaging channel.
	MessagingChannelRead Permission = "messaging:channel_read"
	// MessagingChannelList grants list access over messaging channels.
	MessagingChannelList Permission = "messaging:channel_list"
	// MessagingChannelDelete grants access to delete a messaging channel.
	MessagingChannelDelete Permission = "messaging:channel_delete"
	// MessagingPublish grants access to publish a message to a channel.
	MessagingPublish Permission = "messaging:publish"
	// MessagingSubscribe grants access to subscribe to a channel.
	MessagingSubscribe Permission = "messaging:subscribe"
	// MessagingUnsubscribe grants access to unsubscribe from a channel.
	MessagingUnsubscribe Permission = "messaging:unsubscribe"
	// MessagingWatch grants access to watch a channel / receive notifications.
	MessagingWatch Permission = "messaging:watch"
	// MessagingStats grants read access to messaging statistics.
	MessagingStats Permission = "messaging:stats"
)

const (
	// SchedulerCreate grants access to create a scheduler job.
	SchedulerCreate Permission = "scheduler:create"
	// SchedulerRead grants read access to a scheduler job.
	SchedulerRead Permission = "scheduler:read"
	// SchedulerList grants list access over scheduler jobs.
	SchedulerList Permission = "scheduler:list"
	// SchedulerUpdate grants access to update a scheduler job.
	SchedulerUpdate Permission = "scheduler:update"
	// SchedulerDelete grants access to delete a scheduler job.
	SchedulerDelete Permission = "scheduler:delete"
	// SchedulerTrigger grants access to trigger a scheduler job immediately.
	SchedulerTrigger Permission = "scheduler:trigger"
	// SchedulerPause grants access to pause a scheduler job.
	SchedulerPause Permission = "scheduler:pause"
	// SchedulerResume grants access to resume a scheduler job.
	SchedulerResume Permission = "scheduler:resume"
	// SchedulerCancel grants access to cancel a scheduler job.
	SchedulerCancel Permission = "scheduler:cancel"
	// SchedulerHistory grants read access to a scheduler job's execution history.
	SchedulerHistory Permission = "scheduler:history"
	// SchedulerStatus grants read access to scheduler status.
	SchedulerStatus Permission = "scheduler:status"
)

const (
	// ParametersRead grants read access to a parameter.
	ParametersRead Permission = "parameters:read"
	// ParametersWrite grants write access to a parameter.
	ParametersWrite Permission = "parameters:write"
	// ParametersDelete grants delete access to a parameter.
	ParametersDelete Permission = "parameters:delete"
	// ParametersList grants list access over parameters.
	ParametersList Permission = "parameters:list"
	// ParametersSearch grants access to search parameters by tags.
	ParametersSearch Permission = "parameters:search"
)

const (
	// DynamicSecretsRoleCreate grants access to create a dynamic-secret role.
	DynamicSecretsRoleCreate Permission = "dynamic_secrets:role_create"
	// DynamicSecretsRoleRead grants read access to a dynamic-secret role.
	DynamicSecretsRoleRead Permission = "dynamic_secrets:role_read"
	// DynamicSecretsRoleList grants list access over dynamic-secret roles.
	DynamicSecretsRoleList Permission = "dynamic_secrets:role_list"
	// DynamicSecretsRoleDelete grants access to delete a dynamic-secret role.
	DynamicSecretsRoleDelete Permission = "dynamic_secrets:role_delete"
	// DynamicSecretsGenerate grants access to generate a leased credential.
	DynamicSecretsGenerate Permission = "dynamic_secrets:generate"
	// DynamicSecretsLeaseRenew grants access to renew a credential lease.
	DynamicSecretsLeaseRenew Permission = "dynamic_secrets:lease_renew"
	// DynamicSecretsLeaseRevoke grants access to revoke a credential lease.
	DynamicSecretsLeaseRevoke Permission = "dynamic_secrets:lease_revoke"
	// DynamicSecretsLeaseList grants list access over credential leases.
	DynamicSecretsLeaseList Permission = "dynamic_secrets:lease_list"
)

const (
	// ProjectCreate grants access to create a project.
	ProjectCreate Permission = "project:create"
	// ProjectRead grants read access to a project.
	ProjectRead Permission = "project:read"
	// ProjectList grants list access over projects.
	ProjectList Permission = "project:list"
	// ProjectUpdate grants access to update a project.
	ProjectUpdate Permission = "project:update"
	// ProjectDelete grants access to delete a project.
	ProjectDelete Permission = "project:delete"
	// ProjectGrant grants access to grant an agent a project role.
	ProjectGrant Permission = "project:grant"
	// ProjectRevoke grants access to revoke an agent's project role.
	ProjectRevoke Permission = "project:revoke"
)

// Whole-family wildcard grants for the broker data-plane resources. These are
// resource:* whole-segment wildcards (see IsWildcard): they are NOT registered
// as concrete permissions, but are valid role grants that the RBAC authorizer
// expands against concrete resource:action checks at authorization time. They
// exist so a single grant covers an entire tenant-owned data-plane family
// without enumerating every action.
const (
	// ObjStoreAll grants every objstore action (resource:* wildcard).
	ObjStoreAll Permission = "objstore:*"
	// OrchestratorAll grants every orchestrator action (resource:* wildcard).
	OrchestratorAll Permission = "orchestrator:*"
	// SchedulerAll grants every scheduler action (resource:* wildcard).
	SchedulerAll Permission = "scheduler:*"
	// MessagingAll grants every messaging action (resource:* wildcard).
	MessagingAll Permission = "messaging:*"
	// ParametersAll grants every parameters action (resource:* wildcard).
	ParametersAll Permission = "parameters:*"
	// DynamicSecretsAll grants every dynamic_secrets action (resource:* wildcard).
	DynamicSecretsAll Permission = "dynamic_secrets:*"
	// ProjectAll grants every project action (resource:* wildcard).
	ProjectAll Permission = "project:*"
	// OllamaAll grants every ollama action (resource:* wildcard).
	OllamaAll Permission = "ollama:*"
)

const (
	// OllamaHealth grants read access to Ollama backend health.
	OllamaHealth Permission = "ollama:health"
	// OllamaModelList grants list access over Ollama models.
	OllamaModelList Permission = "ollama:model_list"
	// OllamaModelInfo grants read access to an Ollama model's metadata.
	OllamaModelInfo Permission = "ollama:model_info"
	// OllamaEmbed grants access to generate embeddings via Ollama.
	OllamaEmbed Permission = "ollama:embed"
)

// brokerDataPlaneScopePermissions returns every broker data-plane permission
// declared in this file as a single slice. The init() function below registers
// each permission and tags it ScopeTenantOnly.
func brokerDataPlaneScopePermissions() []Permission {
	return []Permission{
		ObjStorePut, ObjStoreGet, ObjStoreDelete, ObjStoreList, ObjStoreHead,
		ObjStoreCopy, ObjStoreArchive, ObjStoreRestore, ObjStoreVerify,
		ObjStoreReplicate, ObjStoreMigrate, ObjStoreBatch,
		ObjStoreReadPolicy, ObjStoreWritePolicy, ObjStoreReadBackend,
		ObjStoreReadStats, ObjStoreTenantUsage, ObjStoreTenantQuotaRead,
		ObjStoreTenantQuotaWrite, ObjStoreAdmin,

		OrchestratorSpawn, OrchestratorPrompt, OrchestratorStatus,
		OrchestratorClose, OrchestratorList, OrchestratorTaskCreate,
		OrchestratorTaskRead, OrchestratorTaskList, OrchestratorTaskClaim,
		OrchestratorTaskComplete, OrchestratorTaskDecompose,
		OrchestratorArtifactSubmit, OrchestratorArtifactRead,
		OrchestratorJobStart, OrchestratorJobRead, OrchestratorJobList,
		OrchestratorJobCancel, OrchestratorBackendList, OrchestratorDiscover,
		OrchestratorHook, OrchestratorWorkflowRead, OrchestratorWorkflowWrite,
		OrchestratorInference, OrchestratorAgentRegister,
		OrchestratorAgentRead, OrchestratorAgentList, OrchestratorAgentRemove,
		OrchestratorAgentHeartbeat, OrchestratorCacheRead,
		OrchestratorCacheWrite, OrchestratorBroadcast,
		// OrchestratorHealth is DELIBERATELY ABSENT -- see
		// nodeStateScopePermissions below. Do not add it back without reading
		// go-qrdb#183; the scope test enumerates the exceptions and will fail
		// if it returns.
		//
		// OrchestratorStatus is deliberately STILL HERE, and that is a
		// narrowing of what #183 asked for. See nodeStateScopePermissions.

		MessagingChannelCreate, MessagingChannelRead, MessagingChannelList,
		MessagingChannelDelete, MessagingPublish, MessagingSubscribe,
		MessagingUnsubscribe, MessagingWatch, MessagingStats,

		SchedulerCreate, SchedulerRead, SchedulerList, SchedulerUpdate,
		SchedulerDelete, SchedulerTrigger, SchedulerPause, SchedulerResume,
		SchedulerCancel, SchedulerHistory, SchedulerStatus,

		ParametersRead, ParametersWrite, ParametersDelete, ParametersList,
		ParametersSearch,

		DynamicSecretsRoleCreate, DynamicSecretsRoleRead, DynamicSecretsRoleList,
		DynamicSecretsRoleDelete, DynamicSecretsGenerate,
		DynamicSecretsLeaseRenew, DynamicSecretsLeaseRevoke,
		DynamicSecretsLeaseList,

		ProjectCreate, ProjectRead, ProjectList, ProjectUpdate, ProjectDelete,
		ProjectGrant, ProjectRevoke,

		OllamaHealth, OllamaModelList, OllamaModelInfo, OllamaEmbed,
	}
}

// brokerDataPlaneCategories groups the broker data-plane permissions for UI
// rendering. They are appended to the authoritative categories slice in init().
func brokerDataPlaneCategories() []Category {
	return []Category{
		{
			Name:  "Object Storage",
			Group: "Data Services",
			Permissions: []Permission{
				ObjStorePut, ObjStoreGet, ObjStoreDelete, ObjStoreList,
				ObjStoreHead, ObjStoreCopy, ObjStoreArchive, ObjStoreRestore,
				ObjStoreVerify, ObjStoreReplicate, ObjStoreMigrate,
				ObjStoreBatch, ObjStoreReadPolicy, ObjStoreWritePolicy,
				ObjStoreReadBackend, ObjStoreReadStats, ObjStoreTenantUsage,
				ObjStoreTenantQuotaRead, ObjStoreTenantQuotaWrite,
				ObjStoreAdmin,
			},
		},
		{
			Name:  "Orchestrator",
			Group: "AI Services",
			Permissions: []Permission{
				OrchestratorSpawn, OrchestratorPrompt, OrchestratorStatus,
				OrchestratorClose, OrchestratorList, OrchestratorTaskCreate,
				OrchestratorTaskRead, OrchestratorTaskList,
				OrchestratorTaskClaim, OrchestratorTaskComplete,
				OrchestratorTaskDecompose, OrchestratorArtifactSubmit,
				OrchestratorArtifactRead, OrchestratorJobStart,
				OrchestratorJobRead, OrchestratorJobList, OrchestratorJobCancel,
				OrchestratorBackendList, OrchestratorDiscover, OrchestratorHook,
				OrchestratorWorkflowRead, OrchestratorWorkflowWrite,
				OrchestratorHealth, OrchestratorInference,
				OrchestratorAgentRegister, OrchestratorAgentRead,
				OrchestratorAgentList, OrchestratorAgentRemove,
				OrchestratorAgentHeartbeat, OrchestratorCacheRead,
				OrchestratorCacheWrite, OrchestratorBroadcast,
			},
		},
		{
			Name:  "Messaging",
			Group: "Data Services",
			Permissions: []Permission{
				MessagingChannelCreate, MessagingChannelRead,
				MessagingChannelList, MessagingChannelDelete, MessagingPublish,
				MessagingSubscribe, MessagingUnsubscribe, MessagingWatch,
				MessagingStats,
			},
		},
		{
			Name:  "Scheduler",
			Group: "Data Services",
			Permissions: []Permission{
				SchedulerCreate, SchedulerRead, SchedulerList, SchedulerUpdate,
				SchedulerDelete, SchedulerTrigger, SchedulerPause,
				SchedulerResume, SchedulerCancel, SchedulerHistory,
				SchedulerStatus,
			},
		},
		{
			Name:  "Parameters",
			Group: "Data Services",
			Permissions: []Permission{
				ParametersRead, ParametersWrite, ParametersDelete,
				ParametersList, ParametersSearch,
			},
		},
		{
			Name:  "Dynamic Secrets",
			Group: "Security Services",
			Permissions: []Permission{
				DynamicSecretsRoleCreate, DynamicSecretsRoleRead,
				DynamicSecretsRoleList, DynamicSecretsRoleDelete,
				DynamicSecretsGenerate, DynamicSecretsLeaseRenew,
				DynamicSecretsLeaseRevoke, DynamicSecretsLeaseList,
			},
		},
		{
			Name:  "Project",
			Group: "AI Services",
			Permissions: []Permission{
				ProjectCreate, ProjectRead, ProjectList, ProjectUpdate,
				ProjectDelete, ProjectGrant, ProjectRevoke,
			},
		},
		{
			Name:  "Ollama",
			Group: "AI Services",
			Permissions: []Permission{
				OllamaHealth, OllamaModelList, OllamaModelInfo, OllamaEmbed,
			},
		},
	}
}

// nodeStateScopePermissions returns the broker data-plane permissions that
// describe the DAEMON'S OWN state rather than material held on a tenant's
// behalf. They are registered but deliberately NOT tagged ScopeTenantOnly, so
// they take the ScopeSelf default (owner ruling 2026-09-02, go-qrdb#183).
//
// ScopeTenantOnly means "tenant-admin-only, system-admin-denied, no
// cross-tenant", and scope.go states what that rule is FOR: per-tenant barrier
// key custody, which the platform operator must not hold. It is a rule about
// material held on a tenant's behalf.
//
// The health and status of the node's own orchestrator is not that. It is
// process state of the machine the platform operator runs, and tagging it
// tenant-only denied it to the system tenant STRUCTURALLY -- the scope check
// runs after the grant check, so no role, not admin, not owner, could satisfy
// it. The operator persona, which exists to watch the node, was the single
// persona that could not see whether the node was alive:
//
//	GET /api/v1/orchestrator/health
//	403 permission denied: admin cannot health on orchestrator:
//	    tenant-only permission denied to system tenant
//
// These two rode along because brokerDataPlaneScopePermissions() registers the
// whole family in one call. Under ScopeSelf the system-tenant rule lifts them
// across tenants while a tenant-local identity stays confined to its own
// tenant, so nothing widens for a tenant principal.
//
// SCOPED TO THESE TWO ONLY. The rest of the family acts on tenant-owned
// sessions, jobs, tasks and artifacts, and go-qrdb#183 made no claim about
// them; a wider re-scoping was offered to the owner and declined.
// ORCHESTRATORSTATUS IS DELIBERATELY NOT HERE, though go-qrdb#183 asked for
// both. Its declaration one file-section up reads "grants read access to
// orchestration SESSION status", and it sits in the run of session verbs --
// spawn, prompt, status, close, list. On the vocabulary as written it is
// tenant-owned session metadata, and moving it to ScopeSelf would let a
// system-tenant grant read every tenant's session status. That is a widening,
// not a fix, and it is not what the issue was arguing for.
//
// #183's measurement is still real: a daemon-level /orchestrator/status
// endpoint returns the same structural 403. But the cause there is that ONE
// permission is being used for two different resources -- the daemon's status
// and a session's status -- and the fix for that is a separate service-level
// permission, not a scope change on the session one. Adding a permission to
// the canonical vocabulary is an owner call and needs the consumer to remap
// its endpoint, so it is reported back on #183 rather than decided here.
//
// This is the half that is unambiguous, landing on its own.
func nodeStateScopePermissions() []Permission {
	return []Permission{
		OrchestratorHealth,
	}
}

func init() {
	tenantOnly := brokerDataPlaneScopePermissions()
	nodeState := nodeStateScopePermissions()

	// Both sets are REGISTERED; only the first is tagged ScopeTenantOnly.
	// nodeState takes the ScopeSelf default by not being tagged, rather than
	// by an explicit RegisterScope(ScopeSelf, ...) -- scope.go documents
	// ScopeSelf as the default and registering it would suggest the other
	// untagged permissions in this package had been considered and were not.
	Register(tenantOnly...)
	Register(nodeState...)
	RegisterScope(ScopeTenantOnly, tenantOnly...)
	categories = append(categories, brokerDataPlaneCategories()...)
}
