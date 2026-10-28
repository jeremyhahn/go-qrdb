// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import { test as base, type Page } from "@playwright/test";

/** Minimal database info for mock responses. */
export interface MockDatabaseInfo {
  name: string;
  backend: string;
  status: string;
  /** Wire protocol configuration — matches SDK DatabaseInfo.protocol field. */
  protocol?: { mysql: boolean; postgresql: boolean };
  /** Replication factor of a dedicated-raft database. Absent/zero for non-raft. */
  replicas?: number;
  /** The database's dedicated store, which a barrier needs. */
  data_dir?: string;
  /** The per-database barrier, as DatabaseInfo carries it; never a secret. */
  barrier?: { enabled: boolean; mode: string; sharesRequired?: number; sharesTotal?: number };
}

/** Minimal tenant info for mock responses. */
export interface MockTenantInfo {
  id: string;
  name: string;
  quotas?: {
    max_databases: number;
    max_raft_groups: number;
    max_replicas_per_group: number;
    max_total_replicas: number;
    max_dedicated_wal_groups: number;
    max_collections: number;
    max_keys: number;
    max_storage_bytes: number;
    max_database_size_bytes: number;
    max_users: number;
    max_roles: number;
    max_certificates: number;
    max_backups: number;
    max_vector_dims: number;
    max_ts_metrics: number;
  } | null;
  entitlements?: {
    allowed_isolation_modes?: string[];
    allow_dedicated_raft?: boolean;
    allow_volume_storage?: boolean;
    allow_system_managed_barrier?: boolean;
    allow_dedicated_wal?: boolean;
  } | null;
  created_at: string;
  updated_at: string;
}

/** Minimal mock trust anchor for mock responses. */
export interface MockTrustAnchor {
  fingerprint: string;
  subject: string;
  not_before: string;
  not_after: string;
  cert_pem: string;
  added_at: string;
}

/** Minimal mock officer status for mock responses. */
/**
 * A share holder as the Go OfficerBinding returns it: transport.OfficerStatus,
 * snake_case per its json tags. A never-claimed holder carries Go's zero time.
 */
export interface MockOfficerStatus {
  username: string;
  role: "so" | "custodian";
  cert_thumbprint: string;
  share_index: number;
  designated: string;
  factors: string[];
  encryption_kem: string;
  enrolled_at: string;
  claimed_at: string;
  share_pending: boolean;
}

/** Go's zero time.Time, which a never-claimed holder's claimed_at carries. */
export const GO_ZERO_TIME = "0001-01-01T00:00:00Z";

/** Mock state that controls binding responses. */
export interface MockState {
  isFirstRun: boolean;
  wizardState: Record<string, unknown> | null;
  configInitialized: boolean;
  /** Server topology mode — controls whether the Cluster nav item is visible. */
  serverMode: 'standalone' | 'raft';
  /**
   * Saved-profile connection type returned by ListProfiles. The AdminNav
   * Daemon item only renders for "external" profiles (embedded servers run
   * qrdbd in-process — there is no OS process to start/stop). Defaults to
   * "embedded" when omitted.
   */
  profileType?: 'embedded' | 'external';
  /**
   * The SO PIN each SealBinding.Seal call received, in order. The mock
   * appends to it, so a test can assert the PIN the Seal Manager sent.
   */
  sealSoPins?: string[];
  sealStatus: {
    sealed: boolean;
    initialized: boolean;
    sharesRequired: number;
    sharesProvided: number;
    sharesTotal: number;
    strategy?: string;
  };
  unsealProgress: {
    sealed: boolean;
    sharesProvided: number;
    sharesRequired: number;
  };
  embeddedRunning: boolean;
  configDir: string;
  databases: MockDatabaseInfo[];
  tenants: MockTenantInfo[];
  /** Enrolled unseal-domain holders of the system barrier. */
  officers: MockOfficerStatus[];
  /** Enrolled recovery-domain holders of the system barrier. */
  recoveryOfficers?: MockOfficerStatus[];
  /** Enrolled holders of each tenant barrier, keyed by tenant ID. */
  tenantOfficers?: Record<string, MockOfficerStatus[]>;
  /** Enrolled holders of each Shamir database barrier, keyed by database. */
  databaseOfficers?: Record<string, MockOfficerStatus[]>;
  /**
   * Whether the server can build a per-database Shamir barrier: false models a
   * node whose database barrier manager has no share-holder custody wired,
   * which refuses a Shamir create with ShamirCustodyNotConfiguredError.
   * Absent means it can.
   */
  databaseShamirCustody?: boolean;
  /**
   * When each database barrier was first initialized, keyed by database: a
   * passphrase barrier at its create, a Shamir barrier when its custody is
   * initialized. A database absent here has never been initialized, and
   * DatabaseBinding.BarrierStatus reports the Go zero time for it.
   */
  databaseBarrierInitializedAt?: Record<string, string>;
  /**
   * Per-tenant barrier state keyed by tenant ID (for the per-tenant unseal
   * tests). Tenants without an entry report "barrier not initialized" from
   * BarrierStatus / BarrierUnseal.
   */
  tenantBarriers?: Record<string, MockTenantBarrierState>;
  /** Per-tenant trust anchors keyed by tenant ID (default: none registered). */
  trustAnchors?: Record<string, MockTrustAnchor[]>;
  /**
   * When true, MVCCBinding.RestoreToRevision's mock reports the FIRST
   * planned change of the next non-dry-run, non-Force apply as a conflict
   * (left unapplied) instead of applying it -- deterministically exercising
   * KVExplorer's conflict / "Retry with Force" UI path. A Force retry always
   * applies unconditionally regardless of this flag, matching real restore
   * semantics. See wails-mock.ts's MVCCBinding.RestoreToRevision.
   */
  restoreSimulateConflict?: boolean;
  errors: Record<string, string>;
}

/** Mock per-tenant barrier state mirroring tenant.TenantBarrierStatus. */
export interface MockTenantBarrierState {
  sealed: boolean;
  initialized: boolean;
  strategy: string;
  shamir_threshold?: number;
  shamir_total?: number;
  /** The passphrase a passphrase-held tenant barrier unseals with. */
  passphrase?: string;
}

/** Default mock state for a fresh first-run scenario. */
export const defaultMockState: MockState = {
  isFirstRun: true,
  wizardState: null,
  configInitialized: false,
  serverMode: 'standalone',
  sealStatus: {
    sealed: false,
    initialized: false,
    sharesRequired: 0,
    sharesProvided: 0,
    sharesTotal: 0,
  },
  unsealProgress: {
    sealed: true,
    sharesProvided: 0,
    sharesRequired: 2,
  },
  embeddedRunning: false,
  configDir: "/mock/config/dir",
  databases: [],
  tenants: [],
  officers: [],
  errors: {},
};

/**
 * Mock state for a second-launch embedded scenario. The wizard has
 * already been completed with embedded mode, so the auto-start flow
 * in App.svelte will load the wizard state and call startEmbedded().
 */
export const secondLaunchEmbeddedState: MockState = {
  isFirstRun: false,
  wizardState: {
    completed: true,
    serverMode: "embedded",
    embeddedConfig: {
      storageBackend: "pebble",
      // Note: wizardState.serverMode is the wizard's own field, distinct from
      // MockState.serverMode which controls the AdminNav cluster visibility.
      dataDir: "/mock/data/dir",
      raftEnabled: false,
    },
  },
  configInitialized: true,
  serverMode: 'standalone',
  sealStatus: {
    sealed: false,
    initialized: true,
    // A real initialized barrier always has a sealing strategy. The Seal
    // Manager treats a missing/"none" strategy as "encryption not configured"
    // and hides the seal controls, so the default initialized state models a
    // passphrase-protected (single-admin sealable) barrier.
    strategy: "passphrase",
    sharesRequired: 0,
    sharesProvided: 0,
    sharesTotal: 0,
  },
  unsealProgress: {
    sealed: true,
    sharesProvided: 0,
    sharesRequired: 2,
  },
  embeddedRunning: false,
  configDir: "/mock/config/dir",
  databases: [],
  tenants: [
    {
      id: "default",
      name: "Default Tenant",
      quotas: null,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
  ],
  officers: [],
  errors: {},
};

/**
 * Mock state for a raft cluster scenario. The Cluster admin nav item is only
 * visible when serverMode is "raft". Use this state for all cluster-specific
 * tests so that the Cluster sidebar item renders.
 */
export const raftClusterState: MockState = {
  ...secondLaunchEmbeddedState,
  serverMode: 'raft',
};

/**
 * Mock state for a second-launch EXTERNAL-server scenario. The saved profile
 * is type "external", so the AdminNav Daemon item renders (the Daemon panel
 * manages the external qrdbd OS process). Use this for Daemon admin tests.
 */
export const secondLaunchExternalState: MockState = {
  ...secondLaunchEmbeddedState,
  profileType: 'external',
};

/**
 * Mock state for a Shamir-mode barrier with pre-registered officers.
 * Used by tests that exercise the Security Officers panel in SealManager.
 */
export const shamirSealState: MockState = {
  ...secondLaunchEmbeddedState,
  sealStatus: {
    sealed: false,
    initialized: true,
    strategy: "shamir",
    sharesRequired: 3,
    sharesProvided: 0,
    sharesTotal: 5,
  },
  officers: [
    {
      username: "alice",
      role: "so",
      cert_thumbprint: "aabbcc",
      share_index: 1,
      designated: "mtls",
      factors: ["mtls"],
      encryption_kem: "dhkem-p256",
      enrolled_at: "2026-01-01T00:00:00Z",
      claimed_at: "2026-01-02T00:00:00Z",
      share_pending: false,
    },
    {
      username: "bob",
      role: "custodian",
      cert_thumbprint: "ddeeff",
      share_index: 2,
      designated: "mtls",
      factors: ["mtls", "fido2"],
      encryption_kem: "dhkem-x25519",
      enrolled_at: "2026-01-01T00:00:00Z",
      claimed_at: GO_ZERO_TIME,
      share_pending: true,
    },
  ],
};

/**
 * Injects mock Wails bindings into the page before the app loads.
 * The mock reads from `window.__QRDB_MOCK_STATE__` which tests can
 * update via `page.evaluate()`.
 */
async function injectWailsMock(page: Page, state: MockState): Promise<void> {
  await page.addInitScript((initialState: MockState) => {
    // Store mock state on window for runtime access
    const w = window as unknown as Record<string, unknown>;
    w.__QRDB_MOCK_STATE__ = JSON.parse(JSON.stringify(initialState));

    function getState(): MockState {
      return w.__QRDB_MOCK_STATE__ as MockState;
    }

    function maybeReject(method: string): void {
      const s = getState();
      if (s.errors[method]) {
        throw new Error(s.errors[method]);
      }
    }

    // Stateful in-memory stores for CRUD bindings
    const kvStore = new Map<string, string>();

    // The platform's node-keyspace compaction policy, as AdminBinding holds
    // it: null when none is set.
    type CompactionPolicy = { mode?: string; retainRevisions?: number; retainPeriod?: string };
    let nodeCompaction: CompactionPolicy | null = null;

    // Shared MVCC revision history, bridging KVBinding (used by the
    // KVExplorer's ordinary Create/Edit/Delete workflow) and MVCCBinding
    // (used by its "As of revision" browse and Restore workflow), so a
    // plain kv.put/kv.delete is visible to mvcc.range(..., {revision}) and
    // mvcc.restoreToRevision(...) exactly as a real raft.sm_type: mvcc
    // daemon would record it -- one monotonic revision counter shared by
    // every write, KV or MVCC. Each key's history is an ordered list of
    // (revision, value) entries; value === null marks a delete.
    let mvccRevision = 0;
    const revisionHistory = new Map<string, Array<{ revision: number; value: string | null }>>();

    /** Records a write (value !== null) or delete (value === null) for key, bumping the shared revision counter, and returns the new revision. */
    function recordRevision(key: string, value: string | null): number {
      mvccRevision++;
      const hist = revisionHistory.get(key) ?? [];
      hist.push({ revision: mvccRevision, value });
      revisionHistory.set(key, hist);
      return mvccRevision;
    }

    /** Reconstructs key's value as of `revision` (inclusive), or null when the key did not exist yet, or was deleted, by that revision. */
    function valueAtRevision(key: string, revision: number): string | null {
      const hist = revisionHistory.get(key);
      if (!hist) return null;
      let result: string | null = null;
      for (const entry of hist) {
        if (entry.revision > revision) break;
        result = entry.value;
      }
      return result;
    }

    /** The ModRevision of key's most recent write/delete, or 0 if it has no history. */
    function currentModRevision(key: string): number {
      const hist = revisionHistory.get(key);
      return hist && hist.length > 0 ? hist[hist.length - 1].revision : 0;
    }

    /** Decodes a base64 UTF-8 string, mirroring sdk/typescript/src/transform.ts's decodeBase64Utf8 (the wire encoding restoreToRevision's prefix/keys/changes[].key fields use). */
    function decodeB64(b64: string): string {
      const binary = atob(b64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
      return new TextDecoder().decode(bytes);
    }

    /** Encodes a UTF-8 string to base64, mirroring sdk/typescript/src/transform.ts's encodeBase64Utf8. */
    function encodeB64(text: string): string {
      const bytes = new TextEncoder().encode(text);
      let binary = "";
      for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
      return btoa(binary);
    }

    // Cert store: serial → cert record
    let certSerialCounter = 1000;
    const certStore = new Map<string, {
      serial: string;
      username: string;
      tenantId: string;
      issuedAt: string;
      expiresAt: string;
      revoked: boolean;
    }>();

    // Daemon state
    const daemonState = {
      running: false,
      pid: null as number | null,
      logFile: null as string | null,
      logs: [] as string[],
    };

    // Backup store: id -> BackupInfo (snake_case wire shape)
    const backupStore = new Map<string, {
      id: string;
      timestamp: string;
      size: number;
      shard_count: number;
      status: string;
      checksum: string;
      destination: string;
      compression: string;
      encryption: string;
    }>();
    let backupCounter = 0;

    // Lock store: key -> LockInfo (camelCase wire shape; matches LockInfo type)
    const lockStore = new Map<string, {
      key: string;
      fencingToken: number;
      leaseId: number;
      holderId: string;
      reentrant: boolean;
      acquireCount: number;
      ttl: number;
      acquiredAt: number;
    }>();
    let lockFencingCounter = 0;

    // Credential store: name -> { current value + version history }
    interface MockCredentialVersion {
      version: number;
      value: string;
      isCurrent: boolean;
      createdAtUnix: number;
      graceUntilUnix?: number;
    }
    const credentialStore = new Map<string, MockCredentialVersion[]>();
    const userStore = new Map<string, {
      id: string;
      username: string;
      tenant: string;
      lifecycle: string;
      createdAt: string;
      updatedAt: string;
    }>();
    const roleStore = new Map<string, {
      id: string;
      name: string;
      tenant: string;
      permissions: string[];
      createdAt: string;
      updatedAt: string;
    }>();

    // Lease store
    const leaseStore = new Map<number, { id: number; ttl: number; granted_ttl: number; remaining_ttl: number }>();
    let leaseIdCounter = 0;

    // Collection and document stores
    const collectionStore = new Map<string, {
      name: string;
      database: string;
      id_strategy: string;
      field_hash?: string;
      schema?: Record<string, string>;
      document_count: number;
      created_at: string;
    }>();
    const documentStore = new Map<string, Map<string, Record<string, unknown>>>();
    // Use BigInt for document ID generation to avoid JavaScript precision loss
    // on unix_nano-style IDs (which exceed Number.MAX_SAFE_INTEGER).
    let nextDocId = BigInt("1773108930009562724");

    // Counter-based UUID generator (deterministic for tests)
    let uuidCounter = 0;
    function fakeUUID(): string {
      uuidCounter++;
      const hex = uuidCounter.toString(16).padStart(12, "0");
      return `00000000-0000-4000-8000-${hex}`;
    }

    // Watch ID counter for multiple concurrent watches
    let nextWatchId = 0;

    /** Map numeric op types to string labels for BatchBinding. */
    const OP_TYPE_MAP: Record<number, string> = {
      0: "PUT",
      1: "GET",
      2: "DELETE",
      3: "RANGE",
    };

    // Mock Wails runtime for event bus (used by WatchClient)
    w.runtime = {
      EventsOnMultiple: (_eventName: string, _callback: unknown, _maxCallbacks: number) => {
        // Return unsubscribe function
        return () => {};
      },
      EventsEmit: () => {},
      EventsOff: () => {},
    };

    // Create the Wails binding namespace
    w.go = {
      main: {
        ConnectionBinding: {
          IsConnected: async () => true,
          Disconnect: async () => {},
          GetVersion: async () => ({
            version: "0.1.0-test",
            gitCommit: "abc123",
            buildDate: "2026-01-01",
          }),
        },
        WizardBinding: {
          IsFirstRun: async () => {
            maybeReject("WizardBinding.IsFirstRun");
            return getState().isFirstRun;
          },
          SaveState: async (state: unknown) => {
            maybeReject("WizardBinding.SaveState");
            const s = getState();
            s.wizardState = state as Record<string, unknown>;
            s.isFirstRun = false;
          },
          LoadState: async () => {
            maybeReject("WizardBinding.LoadState");
            return getState().wizardState;
          },
          GetConfigDir: async () => {
            maybeReject("WizardBinding.GetConfigDir");
            return getState().configDir;
          },
        },
        SealBinding: {
          // SealBinding.Unseal(secret): credential barriers only. A Shamir
          // barrier opens through a holder quorum, and the real binding
          // refuses this call for it.
          Unseal: async (_secret: string) => {
            maybeReject("SealBinding.Unseal");
            const s = getState();
            if (s.sealStatus.strategy === "shamir") {
              throw new Error("qrdb/seal: a Shamir barrier unseals through a quorum of its share holders");
            }
            s.sealStatus.sealed = false;
            s.unsealProgress.sealed = false;
            return { sealed: false, sharesProvided: 0, sharesRequired: 0 };
          },
          // SealBinding.ChangePassphrase(current, next, soPin): a Shamir
          // barrier has no passphrase, and the real binding refuses it.
          ChangePassphrase: async (_current: string, _next: string, _soPin: string) => {
            maybeReject("SealBinding.ChangePassphrase");
            if (getState().sealStatus.strategy === "shamir") {
              throw new Error("qrdb/seal: a Shamir barrier has no passphrase to change");
            }
          },
          // SealBinding.Seal(soPin): the PIN is recorded so a test can
          // assert what the Seal Manager sent.
          Seal: async (soPin: string) => {
            maybeReject("SealBinding.Seal");
            const s = getState();
            s.sealSoPins = [...(s.sealSoPins ?? []), soPin];
            s.sealStatus.sealed = true;
          },
          Initialize: async (_secret: string) => {
            maybeReject("SealBinding.Initialize");
            const s = getState();
            s.sealStatus.initialized = true;
            s.sealStatus.sealed = false;
            s.configInitialized = true;
          },
          // SealBinding.InitializeShamir(threshold, totalShares, policy):
          // the unseal key is split to exactly the enrolled unseal holders,
          // so the real server refuses a share count that differs from them.
          // The response is share-free.
          InitializeShamir: async (threshold: number, totalShares: number, _policy: string) => {
            maybeReject("SealBinding.InitializeShamir");
            const s = getState();
            if (s.officers.length !== totalShares) {
              throw new Error(
                `qrdb/seal: ${totalShares} shares were requested but ${s.officers.length} holders are enrolled`,
              );
            }
            s.sealStatus.initialized = true;
            s.sealStatus.sealed = false;
            s.sealStatus.sharesRequired = threshold;
            s.sealStatus.sharesTotal = totalShares;
            s.sealStatus.strategy = "shamir";
            s.configInitialized = true;
            s.officers = s.officers.map((o, i) => ({ ...o, share_index: i + 1, share_pending: true }));
            return { rootToken: "", threshold, shareCount: totalShares };
          },
          Status: async () => {
            maybeReject("SealBinding.Status");
            return { ...getState().sealStatus };
          },
        },
        CeremonyBinding: {
          // CeremonyBinding.Initialize(req): `qrdb init`'s desktop
          // counterpart, and the ONLY way to bring up a ceremony-backed
          // system barrier -- BarrierInitialize/BarrierInitializeShamir
          // both refuse it. Every officer and the admin credential travel
          // together in one call; there is no per-holder registration
          // call before it. The response mirrors the Go binding's
          // CeremonyInitializeResult, not the raw SDK response: the
          // admin-generate path never returns an admin key here, only
          // adminKeyPath -- the Go binding saves the key to a local file
          // and never hands the page the raw key.
          Initialize: async (req: {
            threshold?: number;
            officers?: { username: string; role?: string; csr_pem: string }[];
            secret?: string;
            admin_csr_pem?: string;
            admin_cert_pem?: string;
            admin_generate?: boolean;
          }) => {
            maybeReject("CeremonyBinding.Initialize");
            const s = getState();
            const officers = req.officers ?? [];
            const isMofN = (req.threshold ?? 0) >= 2;
            if (isMofN && officers.length === 0) {
              throw new Error("ceremony: at least one officer is required for M-of-N");
            }
            const adminPathsSelected = [req.admin_csr_pem, req.admin_cert_pem, req.admin_generate ? "generate" : undefined]
              .filter((v) => v !== undefined && v !== "").length;
            if (adminPathsSelected !== 1) {
              throw new Error("ceremony: exactly one admin credential path (CSR, certificate or generate) is required");
            }

            s.sealStatus.initialized = true;
            s.sealStatus.sealed = false;
            s.configInitialized = true;
            if (isMofN) {
              s.sealStatus.strategy = "shamir";
              s.sealStatus.sharesRequired = req.threshold ?? 0;
              s.sealStatus.sharesTotal = officers.length;
              s.officers = officers.map((o, i) => ({
                username: o.username,
                role: o.role === "custodian" ? "custodian" : "so",
                cert_thumbprint: `mock${o.username}`,
                share_index: i + 1,
                designated: "mtls",
                factors: ["mtls"],
                encryption_kem: "",
                enrolled_at: new Date().toISOString(),
                claimed_at: "0001-01-01T00:00:00Z",
                share_pending: true,
              }));
            } else {
              s.sealStatus.strategy = req.secret ? "passphrase" : "none";
            }

            const holders = officers.map((o) => ({
              username: o.username,
              role: o.role === "custodian" ? "custodian" : "so",
              certPem: `-----BEGIN CERTIFICATE-----\nmock-holder-cert-for-${o.username}\n-----END CERTIFICATE-----`,
            }));
            const adminGenerate = req.admin_generate === true;
            return {
              spkiPin: "mockpin",
              caCertPem: "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
              tlsCertPem: "-----BEGIN CERTIFICATE-----\ntls\n-----END CERTIFICATE-----",
              state: holders.length > 0 ? "enrolling" : "operational",
              shareCount: holders.length,
              holders,
              adminCertPem: "-----BEGIN CERTIFICATE-----\nadmin\n-----END CERTIFICATE-----",
              ...(adminGenerate ? { adminKeyPath: "/mock/config/dir/qrdb/admin-key-mock.pem" } : {}),
              adminCredentialSource: adminGenerate ? "generated" : req.admin_csr_pem ? "csr" : "byoc",
            };
          },
          Status: async () => {
            maybeReject("CeremonyBinding.Status");
            return {
              state: getState().configInitialized ? "operational" : "awaiting_init",
              spkiPin: "mockpin",
            };
          },
          SignCSR: async (req: { username: string; so_pin: string; csr_pem: string; role: string }) => {
            maybeReject("CeremonyBinding.SignCSR");
            return { certPem: `-----BEGIN CERTIFICATE-----\nmock-signed-${req.username}\n-----END CERTIFICATE-----` };
          },
        },
        OfficerBinding: (() => {
          // Mirrors the Go OfficerBinding: RegisterHolder, ListHolders,
          // ClaimEnvelope, SubmitShare, StartRekey and RevokeHolder, each over
          // a HolderScope ({tenant_id}, {database} or {domain}), and
          // InitializeDatabaseShamir. Arguments arrive snake_case, as the SDK
          // sends them; results are the snake_case transport structs the
          // real binding returns.
          type Scope = { tenant_id?: string; database?: string; domain?: string };
          // A database scope names a Shamir database, as the database
          // service's holder custody requires.
          const shamirDatabase = (name: string): Record<string, unknown> => {
            const db = getState().databases.find((d: { name: string }) => d.name === name) as
              | (Record<string, unknown> & { barrier?: { mode?: string } })
              | undefined;
            if (!db || db.barrier?.mode !== "shamir") {
              throw new Error(`database: database "${name}" has no shamir barrier`);
            }
            return db;
          };
          const holdersOf = (scope: Scope): MockOfficerStatus[] => {
            const s = getState();
            if (scope.tenant_id && scope.database) {
              throw new Error("invalid scope: names both a tenant and a database");
            }
            if (scope.database) {
              shamirDatabase(scope.database);
              s.databaseOfficers = s.databaseOfficers ?? {};
              s.databaseOfficers[scope.database] = s.databaseOfficers[scope.database] ?? [];
              return s.databaseOfficers[scope.database]!;
            }
            if (scope.tenant_id) {
              s.tenantOfficers = s.tenantOfficers ?? {};
              s.tenantOfficers[scope.tenant_id] = s.tenantOfficers[scope.tenant_id] ?? [];
              return s.tenantOfficers[scope.tenant_id]!;
            }
            if (scope.domain === "recovery") {
              s.recoveryOfficers = s.recoveryOfficers ?? [];
              return s.recoveryOfficers;
            }
            return s.officers;
          };
          const round = (scope: Scope, threshold: number, policy: string, remove: string[]) => ({
            domain: scope.tenant_id || scope.database ? "unseal" : (scope.domain ?? "unseal"),
            op: "rekey",
            required: getState().sealStatus.sharesRequired || 2,
            submitted: 0,
            expires_at: new Date(Date.now() + 10 * 60 * 1000).toISOString(),
            ...(threshold > 0 ? { new_threshold: threshold } : {}),
            ...(policy ? { policy } : {}),
            ...(remove.length > 0 ? { remove } : {}),
          });
          return {
            RegisterHolder: async (
              scope: Scope,
              req: { username: string; role?: string; csr_pem: string; factors?: { name: string }[]; designated?: string },
            ) => {
              maybeReject("OfficerBinding.RegisterHolder");
              if (!req.username || !req.csr_pem) throw new Error("ceremony: username and CSR are required");
              const holders = holdersOf(scope);
              if (holders.some((h) => h.username === req.username)) {
                throw new Error(`ceremony: holder "${req.username}" already enrolled`);
              }
              // Mirrors holderScope.refuseOverCapacity: before initialization
              // a database barrier takes exactly N holders and no more.
              if (scope.database) {
                const db = shamirDatabase(scope.database) as Record<string, unknown> & {
                  barrier: { sharesTotal: number };
                };
                const initialized = getState().databaseBarrierInitializedAt?.[scope.database] !== undefined;
                if (!initialized && holders.length >= db.barrier.sharesTotal) {
                  throw new Error(
                    `ceremony: all ${db.barrier.sharesTotal} share holders the barrier is split to are enrolled; no more can be enrolled before it is initialized`,
                  );
                }
              }
              const role = req.role === "custodian" ? "custodian" : "so";
              const factors = ["mtls", ...(req.factors ?? []).map((f) => f.name)];
              holders.push({
                username: req.username,
                role,
                cert_thumbprint: `mock${req.username}`,
                share_index: 0,
                designated: req.designated ?? "mtls",
                factors,
                encryption_kem: "dhkem-p256",
                enrolled_at: new Date().toISOString(),
                // In-page code: module constants are not in scope here.
                claimed_at: "0001-01-01T00:00:00Z",
                share_pending: false,
              });
              return {
                cert_pem: `-----BEGIN CERTIFICATE-----\nmock-holder-cert-for-${req.username}\n-----END CERTIFICATE-----`,
                role,
                designated: req.designated ?? "mtls",
                encryption_kem: "dhkem-p256",
                factors,
              };
            },
            ListHolders: async (scope: Scope) => {
              maybeReject("OfficerBinding.ListHolders");
              return holdersOf(scope).map((h) => ({ ...h }));
            },
            ClaimEnvelope: async (_req: Scope & { username: string; key_pem: string }) => {
              maybeReject("OfficerBinding.ClaimEnvelope");
              return { share_index: 1, envelope: "bW9jay1lbnZlbG9wZQ==" };
            },
            SubmitShare: async (req: Scope & { op: string; username: string }) => {
              maybeReject("OfficerBinding.SubmitShare");
              return { domain: req.domain ?? "unseal", op: req.op, required: 2, submitted: 1, complete: false };
            },
            StartRekey: async (req: Scope & { threshold: number; policy?: string; so_pin?: string }) => {
              maybeReject("OfficerBinding.StartRekey");
              return round(req, req.threshold, req.policy ?? "", []);
            },
            RevokeHolder: async (req: Scope & { username: string; threshold: number; so_pin?: string }) => {
              maybeReject("OfficerBinding.RevokeHolder");
              if (!holdersOf(req).some((h) => h.username === req.username)) {
                throw new Error(`ceremony: holder "${req.username}" not found`);
              }
              return round(req, req.threshold, "", [req.username]);
            },
            // Mirrors DatabaseService.DatabaseBarrierOfficerUnenrol: a holder
            // is removed before initialization only; after it, by revocation.
            UnenrolDatabaseHolder: async (database: string, username: string) => {
              maybeReject("OfficerBinding.UnenrolDatabaseHolder");
              if (!username) throw new Error("ceremony: username is required");
              const holders = holdersOf({ database });
              if (getState().databaseBarrierInitializedAt?.[database] !== undefined) {
                throw new Error("ceremony: a holder of an initialized barrier is removed by revocation, not unenrolment");
              }
              const i = holders.findIndex((h) => h.username === username);
              if (i < 0) throw new Error("ceremony: officer not found");
              holders.splice(i, 1);
            },
            // Mirrors DatabaseService.DatabaseBarrierShamirInitialize: a
            // barrier is initialized once (seal.ErrAlreadyInitialized), with
            // exactly sharesTotal holders enrolled (seal.HolderCountMismatchError);
            // the split is the M-of-N the database was created with, and the
            // barrier is left unsealed.
            InitializeDatabaseShamir: async (database: string, _policy: string) => {
              maybeReject("OfficerBinding.InitializeDatabaseShamir");
              const db = shamirDatabase(database) as Record<string, unknown> & {
                barrier: { sharesRequired: number; sharesTotal: number };
              };
              const s = getState();
              if (s.databaseBarrierInitializedAt?.[database] !== undefined) {
                throw new Error("qrdb/seal: barrier already initialized");
              }
              const holders = holdersOf({ database });
              if (holders.length !== db.barrier.sharesTotal) {
                throw new Error(
                  `qrdb/seal: ${db.barrier.sharesTotal} shares were requested but ${holders.length} holders are enrolled`,
                );
              }
              s.databaseBarrierInitializedAt = { ...(s.databaseBarrierInitializedAt ?? {}), [database]: new Date().toISOString() };
              holders.forEach((h, i) => {
                h.share_index = i + 1;
                h.share_pending = true;
              });
              db.status = "open";
              return {
                threshold: db.barrier.sharesRequired,
                total_shares: db.barrier.sharesTotal,
                share_count: holders.length,
              };
            },
          };
        })(),
        EmbeddedServerBinding: {
          StartEmbedded: async (_config: unknown) => {
            maybeReject("EmbeddedServerBinding.StartEmbedded");
            const s = getState();
            s.embeddedRunning = true;
          },
          StopEmbedded: async () => {
            maybeReject("EmbeddedServerBinding.StopEmbedded");
            const s = getState();
            s.embeddedRunning = false;
          },
          IsEmbedded: () => getState().embeddedRunning,
        },
        KVBinding: {
          Put: async (key: string, value: string) => {
            maybeReject("KVBinding.Put");
            kvStore.set(key, value);
            recordRevision(key, value);
          },
          Get: async (key: string) => {
            maybeReject("KVBinding.Get");
            const val = kvStore.get(key);
            if (val === undefined) {
              throw new Error(`key "${key}" not found`);
            }
            return val;
          },
          Delete: async (key: string) => {
            maybeReject("KVBinding.Delete");
            kvStore.delete(key);
            recordRevision(key, null);
          },
          List: async (prefix: string) => {
            maybeReject("KVBinding.List");
            const keys: string[] = [];
            for (const k of kvStore.keys()) {
              if (!prefix || k.startsWith(prefix)) {
                keys.push(k);
              }
            }
            return keys;
          },
          Exists: async (key: string) => {
            maybeReject("KVBinding.Exists");
            return kvStore.has(key);
          },
          Scan: async (prefix: string) => {
            maybeReject("KVBinding.Scan");
            // Return a Record<string, string> mapping key → value, as expected by
            // the AuditLog component's `client.kv.scan()` call.
            const result: Record<string, string> = {};
            for (const [k, v] of kvStore.entries()) {
              if (!prefix || k.startsWith(prefix)) {
                result[k] = v;
              }
            }
            return result;
          },
        },
        AdminBinding: {
          Health: async () => {
            maybeReject("AdminBinding.Health");
            return { status: "ok" };
          },
          Status: async () => {
            maybeReject("AdminBinding.Status");
            return { node: {}, shards: [] };
          },
          Peers: async () => {
            maybeReject("AdminBinding.Peers");
            return { shards: [] };
          },
          AddMember: async () => {
            maybeReject("AdminBinding.AddMember");
          },
          RemoveMember: async () => {
            maybeReject("AdminBinding.RemoveMember");
          },
          RequestSnapshot: async () => {
            maybeReject("AdminBinding.RequestSnapshot");
          },
          Drain: async () => {
            maybeReject("AdminBinding.Drain");
          },
          Capabilities: async () => {
            maybeReject("AdminBinding.Capabilities");
            return { features: [] };
          },
          // NodeCompaction and SetNodeCompaction mirror AdminBinding: the Go
          // *DatabaseCompactionConfig is null when no policy is set, and a
          // null or mode-less policy turns it off.
          NodeCompaction: async () => {
            maybeReject("AdminBinding.NodeCompaction");
            return nodeCompaction;
          },
          SetNodeCompaction: async (policy: CompactionPolicy | null) => {
            maybeReject("AdminBinding.SetNodeCompaction");
            nodeCompaction = policy && policy.mode && policy.mode !== "off" ? { ...policy } : null;
            return nodeCompaction;
          },
          GetTopology: async () => {
            maybeReject("AdminBinding.GetTopology");
            return {
              node_id: "node-1",
              raft_address: "127.0.0.1:63001",
              shard_count: 1,
              shards: [
                {
                  shard_id: 1,
                  replica_id: 1,
                  is_leader: true,
                  leader_id: 1,
                  nodes: { "1": "127.0.0.1:63001" },
                },
              ],
            };
          },
        },
        MVCCBinding: {
          PutWithLease: async (key: string, value: string, leaseId?: number) => {
            maybeReject("MVCCBinding.PutWithLease");
            kvStore.set(key, value);
            const rev = recordRevision(key, value);
            return { revision: rev, leaseId: leaseId ?? 0 };
          },
          // MVCCKeyValue's key/value fields carry the same binary-safe
          // base64 wire encoding as RestoreKeyChange.key (see
          // sdk/typescript/src/transform.ts's decodeMVCCKV, which every
          // caller of GetRevision/GetAtRevision/Range unconditionally
          // base64-decodes) -- so, unlike KVBinding's plaintext strings,
          // every key/value this binding returns must be base64-encoded
          // here to round-trip correctly.
          GetRevision: async (key: string) => {
            maybeReject("MVCCBinding.GetRevision");
            const val = kvStore.get(key);
            if (val === undefined) throw new Error(`key "${key}" not found`);
            return { key: encodeB64(key), value: encodeB64(val), revision: currentModRevision(key), version: 1 };
          },
          GetAtRevision: async (key: string, revision: number) => {
            maybeReject("MVCCBinding.GetAtRevision");
            const val = valueAtRevision(key, revision);
            if (val === null) throw new Error(`key "${key}" not found at revision ${revision}`);
            return { key: encodeB64(key), value: encodeB64(val), revision };
          },
          // start/end select the live-key range as usual; a truthy
          // opts.revision (the ONLY field KVExplorer's as-of browse sends,
          // via client.mvcc.range(start, end, {revision})) switches to a
          // historical reconstruction from revisionHistory instead of the
          // live kvStore, so a caller can browse the keyspace exactly as it
          // looked at that revision -- including keys since deleted, and
          // excluding keys not yet created.
          Range: async (start: string, end: string, opts?: { revision?: number }) => {
            maybeReject("MVCCBinding.Range");
            const asOf = opts && typeof opts.revision === "number" && opts.revision > 0 ? opts.revision : null;
            const kvs: { key: string; value: string }[] = [];
            if (asOf !== null) {
              for (const key of revisionHistory.keys()) {
                if (key < start || (end !== "" && key >= end)) continue;
                const val = valueAtRevision(key, asOf);
                if (val !== null) kvs.push({ key, value: val });
              }
              kvs.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
            } else {
              for (const [k, v] of kvStore.entries()) {
                if (k >= start && (end === "" || k < end)) {
                  kvs.push({ key: k, value: v });
                }
              }
            }
            return { kvs: kvs.map((kv) => ({ key: encodeB64(kv.key), value: encodeB64(kv.value) })), count: kvs.length };
          },
          Compact: async (_revision: number) => {
            maybeReject("MVCCBinding.Compact");
          },
          CurrentRevision: async () => {
            maybeReject("MVCCBinding.CurrentRevision");
            return mvccRevision;
          },
          // Mirrors the real MVCCBinding.RestoreToRevision (cmd/qrdb/bindings_mvcc.go):
          // req arrives snake_case (prefix/keys base64-encoded, matching the
          // SDK's wire convention for []byte fields -- see
          // sdk/typescript/src/wails.ts's restoreToRevision), and the
          // response is returned snake_case too (from_revision/to_revision/
          // value_bytes/dry_run) -- NOT pre-camelCased here -- because the
          // SDK's wrapBindings() Proxy (wails.ts) already applies
          // fromSnake()/deepSnakeToCamel() to every MVCCBinding call's
          // result before the Svelte component ever sees it, exactly as it
          // does for the real backend's Go-json-tagged response. Returning
          // already-camelCase fields here would double-transform nothing
          // (single words are unaffected) but would misrepresent the wire
          // shape this mock exists to stand in for.
          RestoreToRevision: async (req: {
            revision: number;
            prefix?: string;
            keys?: string[];
            dry_run?: boolean;
            force?: boolean;
          }) => {
            maybeReject("MVCCBinding.RestoreToRevision");
            const revision = Number(req.revision) || 0;
            const dryRun = Boolean(req.dry_run);
            const force = Boolean(req.force);
            const prefix = req.prefix !== undefined ? decodeB64(req.prefix) : undefined;
            const explicitKeys = Array.isArray(req.keys) ? req.keys.map(decodeB64) : undefined;

            // Candidate set: every key this mock has EVER recorded history
            // for (live now or deleted since), scoped by prefix or the
            // explicit key list -- mirrors the real server's use of the
            // durable revision index to find keys deleted since the target
            // revision (pkg/service/mvcc_restore.go's planRestorePrefix doc
            // comment), which is what lets a prefix restore recreate a
            // bulk-deleted key.
            let candidates: string[];
            if (explicitKeys) {
              candidates = explicitKeys;
            } else {
              candidates = [];
              for (const key of revisionHistory.keys()) {
                if (prefix === undefined || key.startsWith(prefix)) candidates.push(key);
              }
            }
            candidates = Array.from(new Set(candidates)).sort();

            type Change = {
              key: string;
              action: "revert" | "recreate" | "delete";
              from_revision: number;
              to_revision: number;
              value_bytes: number;
            };
            const changes: Change[] = [];
            let skipped = 0;
            for (const key of candidates) {
              const was = valueAtRevision(key, revision);
              const is = kvStore.has(key) ? (kvStore.get(key) as string) : null;
              if (was !== null && is !== null) {
                if (was === is) {
                  skipped++;
                  continue;
                }
                changes.push({
                  key: encodeB64(key),
                  action: "revert",
                  from_revision: currentModRevision(key),
                  to_revision: revision,
                  value_bytes: was.length,
                });
              } else if (was !== null) {
                changes.push({
                  key: encodeB64(key),
                  action: "recreate",
                  from_revision: 0,
                  to_revision: revision,
                  value_bytes: was.length,
                });
              } else if (is !== null) {
                changes.push({
                  key: encodeB64(key),
                  action: "delete",
                  from_revision: currentModRevision(key),
                  to_revision: 0,
                  value_bytes: 0,
                });
              } else {
                skipped++;
              }
            }

            if (dryRun) {
              return { revision, changes, applied: 0, skipped, conflicted: 0, dry_run: true };
            }

            // restoreSimulateConflict lets a test deterministically exercise
            // the conflict/"Retry with Force" path without a real
            // distributed race (that race is covered end-to-end by
            // test/integration/kv_restore_cli_test.go's conflict subtest):
            // when set, and Force is NOT set, the first computed change is
            // reported as a conflict (left unapplied) instead of applied.
            // A subsequent Force retry always applies everything
            // unconditionally, exactly like the real server.
            const simulateConflict = Boolean(getState().restoreSimulateConflict) && !force;
            const conflicts: Change[] = [];
            let applied = 0;
            changes.forEach((c, i) => {
              if (simulateConflict && i === 0) {
                conflicts.push(c);
                return;
              }
              const key = decodeB64(c.key);
              if (c.action === "delete") {
                kvStore.delete(key);
                recordRevision(key, null);
              } else {
                const val = valueAtRevision(key, revision) as string;
                kvStore.set(key, val);
                recordRevision(key, val);
              }
              applied++;
            });

            return {
              revision,
              changes,
              conflicts: conflicts.length > 0 ? conflicts : undefined,
              applied,
              skipped,
              conflicted: conflicts.length,
              dry_run: false,
            };
          },
        },
        TransactionBinding: {
          Execute: async () => {
            maybeReject("TransactionBinding.Execute");
            return { succeeded: true, responses: [] };
          },
        },
        BatchBinding: {
          Execute: async (ops: { type: number | string; key: string; value?: string }[]) => {
            maybeReject("BatchBinding.Execute");
            const responses = ops.map((op: { type: number | string; key: string; value?: string }) => {
              // Handle both numeric (from SDK) and string op types
              const typeStr = typeof op.type === "number"
                ? (OP_TYPE_MAP[op.type] ?? "UNKNOWN")
                : String(op.type).toUpperCase();
              switch (typeStr) {
                case "PUT":
                  kvStore.set(op.key, op.value ?? "");
                  return { key: op.key, value: "" };
                case "GET": {
                  const val = kvStore.get(op.key);
                  return { key: op.key, value: val ?? "" };
                }
                case "DELETE":
                  kvStore.delete(op.key);
                  return { key: op.key, value: "" };
                default:
                  return { key: op.key, value: "" };
              }
            });
            return { responses };
          },
          ImportFile: async (_filePath: string, _opts: unknown) => {
            maybeReject("BatchBinding.ImportFile");
            return { total: 3, succeeded: 3, failed: 0, errors: [] };
          },
          AvailableCodecs: async () => {
            maybeReject("BatchBinding.AvailableCodecs");
            return ["avro", "bson", "json", "msgpack", "protobuf", "yaml"];
          },
          AvailableImportFormats: async () => {
            maybeReject("BatchBinding.AvailableImportFormats");
            return ["auto", "csv", "json", "ndjson", "yaml", "archive"];
          },
        },
        WatchBinding: {
          Watch: async () => {
            maybeReject("WatchBinding.Watch");
            return ++nextWatchId;
          },
          WatchPrefix: async () => {
            maybeReject("WatchBinding.WatchPrefix");
            return ++nextWatchId;
          },
          Unwatch: async () => {
            maybeReject("WatchBinding.Unwatch");
          },
          CancelWatch: async () => {
            maybeReject("WatchBinding.Unwatch");
          },
        },
        IndexBinding: (() => {
          // Index metadata store: indexName → { fieldName, unique, sparse, numeric, entries }
          //
          // entries is the inverted index: fieldValue → docKeys[].
          // It is keyed by the stringified field value so that both numeric
          // and string fields round-trip correctly through JSON comparisons.
          const indexStore = new Map<string, {
            name: string;
            fieldName: string;
            unique: boolean;
            sparse: boolean;
            numeric: boolean;
            entries: Map<string, string[]>;
          }>();

          /** Stringify any scalar field value to a stable lookup key. */
          function fieldKey(value: unknown): string {
            if (value === null || value === undefined) return "";
            if (typeof value === "object") return JSON.stringify(value);
            return String(value);
          }

          /**
           * Add a single document to every index whose name starts with
           * "{database}.{collection}.".  Called by InsertDocument after the
           * document has been persisted.
           */
          function indexDocument(
            database: string,
            collection: string,
            docId: string,
            doc: Record<string, unknown>,
          ): void {
            const prefix = `${database}.${collection}.`;
            for (const [, idx] of indexStore.entries()) {
              if (!idx.name.startsWith(prefix)) continue;
              const rawValue = doc[idx.fieldName];
              // Sparse indexes skip documents that are missing the field.
              if (rawValue === undefined || rawValue === null) {
                if (idx.sparse) continue;
              }
              const fk = fieldKey(rawValue);
              if (fk === "") continue;
              let keys = idx.entries.get(fk);
              if (keys === undefined) {
                keys = [];
                idx.entries.set(fk, keys);
              }
              if (!keys.includes(docId)) keys.push(docId);
            }
          }

          return {
            Create: async (req: { name: string; field_name: string; unique?: boolean; sparse?: boolean; numeric?: boolean }) => {
              maybeReject("IndexBinding.Create");
              const entries = new Map<string, string[]>();
              // Backfill from any documents that already exist in the collection.
              // The index name format is "{database}.{collection}.{field}".
              const parts = req.name.split(".");
              if (parts.length >= 3) {
                const database = parts[0];
                const collection = parts[1];
                const collKey = `${database}/${collection}`;
                const existingDocs = documentStore.get(collKey);
                if (existingDocs !== undefined) {
                  for (const [docId, doc] of existingDocs.entries()) {
                    const rawValue = doc[req.field_name];
                    if (rawValue === undefined || rawValue === null) {
                      if (req.sparse ?? false) continue;
                    }
                    const fk = fieldKey(rawValue);
                    if (fk === "") continue;
                    let keys = entries.get(fk);
                    if (keys === undefined) {
                      keys = [];
                      entries.set(fk, keys);
                    }
                    keys.push(docId);
                  }
                }
              }
              indexStore.set(req.name, {
                name: req.name,
                fieldName: req.field_name,
                unique: req.unique ?? false,
                sparse: req.sparse ?? false,
                numeric: req.numeric ?? false,
                entries,
              });
            },
            Drop: async (name: string) => {
              maybeReject("IndexBinding.Drop");
              if (!indexStore.has(name)) throw new Error(`index "${name}" not found`);
              indexStore.delete(name);
            },
            Query: async (indexName: string, value: string) => {
              maybeReject("IndexBinding.Query");
              const idx = indexStore.get(indexName);
              if (idx === undefined) return { keys: [] };
              if (idx.numeric) {
                // Numeric index: compare parsed integers so "123" matches stored key "123"
                const queryNum = Number(value);
                for (const [storedKey, keys] of idx.entries.entries()) {
                  if (Number(storedKey) === queryNum) {
                    return { keys: [...keys] };
                  }
                }
                return { keys: [] };
              }
              const keys = idx.entries.get(value) ?? [];
              return { keys: [...keys] };
            },
            Scan: async (indexName: string, start: string, end: string) => {
              maybeReject("IndexBinding.Scan");
              const idx = indexStore.get(indexName);
              if (idx === undefined) return { entries: [] };
              const result: { value: string; keys: string[] }[] = [];
              if (idx.numeric) {
                // Numeric range scan: start <= numericValue < end
                const startNum = Number(start);
                const endNum = Number(end);
                for (const [value, keys] of idx.entries.entries()) {
                  const n = Number(value);
                  if (n >= startNum && n < endNum) {
                    result.push({ value, keys: [...keys] });
                  }
                }
                // Return in numeric order for deterministic test assertions.
                result.sort((a, b) => Number(a.value) - Number(b.value));
              } else {
                // Lexicographic range scan: start <= value < end
                for (const [value, keys] of idx.entries.entries()) {
                  if (value >= start && value < end) {
                    result.push({ value, keys: [...keys] });
                  }
                }
                result.sort((a, b) => (a.value < b.value ? -1 : a.value > b.value ? 1 : 0));
              }
              return { entries: result };
            },
            List: async () => {
              maybeReject("IndexBinding.List");
              return { names: Array.from(indexStore.keys()) };
            },
            // Expose the internal helpers so CollectionBinding.InsertDocument
            // can call them without a second Map declaration.
            __indexDocument: indexDocument,
            __indexStore: indexStore,
          };
        })(),
        LeaseBinding: {
          Grant: async (ttl: number) => {
            maybeReject("LeaseBinding.Grant");
            const id = ++leaseIdCounter;
            leaseStore.set(id, { id, ttl, granted_ttl: ttl, remaining_ttl: ttl });
            return { id, ttl, granted_ttl: ttl, remaining_ttl: ttl };
          },
          Revoke: async (id: number) => {
            maybeReject("LeaseBinding.Revoke");
            if (!leaseStore.has(id)) throw new Error(`lease ${id} not found`);
            leaseStore.delete(id);
          },
          KeepAlive: async (id: number) => {
            maybeReject("LeaseBinding.KeepAlive");
            const lease = leaseStore.get(id);
            if (!lease) throw new Error(`lease ${id} not found`);
            // Reset TTL back to granted_ttl on keep-alive
            lease.ttl = lease.granted_ttl;
            lease.remaining_ttl = lease.granted_ttl;
            return { ...lease };
          },
          TimeToLive: async (id: number) => {
            maybeReject("LeaseBinding.TimeToLive");
            const lease = leaseStore.get(id);
            if (!lease) throw new Error(`lease ${id} not found`);
            return { ...lease };
          },
          List: async () => {
            maybeReject("LeaseBinding.List");
            return Array.from(leaseStore.values());
          },
        },
        TimeSeriesBinding: (() => {
          const tsPoints: Array<{ metric: string; value: number; timestampNs: number; tags?: { key: string; value: string }[] }> = [];
          const tsRetentionStore = new Map<string, number>();
          const tsDeletedMetrics = new Set<string>();
          return {
            Write: async (metric: string, value: number, timestampNs: number) => {
              maybeReject("TimeSeriesBinding.Write");
              if (!tsDeletedMetrics.has(metric)) {
                tsPoints.push({ metric, value, timestampNs });
              }
            },
            WriteBatch: async (points: Array<{ metric: string; value: number; timestampNs: number }>) => {
              maybeReject("TimeSeriesBinding.WriteBatch");
              for (const p of points) {
                if (!tsDeletedMetrics.has(p.metric)) {
                  tsPoints.push({ ...p });
                }
              }
            },
            WritePoint: async (point: Record<string, unknown>) => {
              maybeReject("TimeSeriesBinding.WritePoint");
              const metric = point["metric"] as string;
              const tsNs = (point["timestamp_ns"] ?? point["timestampNs"]) as number;
              if (!tsDeletedMetrics.has(metric)) {
                tsPoints.push({
                  metric,
                  value: point["value"] as number,
                  timestampNs: tsNs,
                  tags: point["tags"] as { key: string; value: string }[] | undefined,
                });
              }
            },
            Query: async (req: Record<string, unknown>) => {
              maybeReject("TimeSeriesBinding.Query");
              const startNs = (req["start_ns"] ?? req["startNs"]) as number;
              const endNs = (req["end_ns"] ?? req["endNs"]) as number;
              const filtered = tsPoints.filter(
                (p) => p.metric === req["metric"] && p.timestampNs >= startNs && p.timestampNs <= endNs,
              );
              return { points: filtered.map((p) => ({ metric: p.metric, value: p.value, timestampNs: p.timestampNs, tags: p.tags ?? [] })) };
            },
            Aggregate: async (req: Record<string, unknown>) => {
              maybeReject("TimeSeriesBinding.Aggregate");
              const startNs = (req["start_ns"] ?? req["startNs"]) as number;
              const endNs = (req["end_ns"] ?? req["endNs"]) as number;
              const filtered = tsPoints.filter(
                (p) => p.metric === req["metric"] && p.timestampNs >= startNs && p.timestampNs <= endNs,
              );
              if (filtered.length === 0) return { result: 0 };
              const values = filtered.map((p) => p.value);
              switch (req["aggregation"]) {
                case "sum": return { result: values.reduce((a: number, b: number) => a + b, 0) };
                case "avg": return { result: values.reduce((a: number, b: number) => a + b, 0) / values.length };
                case "min": return { result: Math.min(...values) };
                case "max": return { result: Math.max(...values) };
                case "count": return { result: values.length };
                default: return { result: 0 };
              }
            },
            RichQuery: async () => {
              maybeReject("TimeSeriesBinding.RichQuery");
              return { series: [] };
            },
            DeleteSeries: async (req: { metric: string }) => {
              maybeReject("TimeSeriesBinding.DeleteSeries");
              let deleted = 0;
              for (let i = tsPoints.length - 1; i >= 0; i--) {
                if (tsPoints[i].metric === req.metric) {
                  tsPoints.splice(i, 1);
                  deleted++;
                }
              }
              tsRetentionStore.delete(req.metric);
              tsDeletedMetrics.add(req.metric);
              return { deleted };
            },
            GetRetention: async (req: { metric: string }) => {
              maybeReject("TimeSeriesBinding.GetRetention");
              return { days: tsRetentionStore.get(req.metric) ?? 0 };
            },
            SetRetention: async (req: { metric: string; days: number }) => {
              maybeReject("TimeSeriesBinding.SetRetention");
              tsRetentionStore.set(req.metric, req.days);
            },
            ListMetrics: async (req?: { prefix?: string }) => {
              maybeReject("TimeSeriesBinding.ListMetrics");
              const metricSet = new Set<string>();
              for (const p of tsPoints) {
                if (!tsDeletedMetrics.has(p.metric)) {
                  metricSet.add(p.metric);
                }
              }
              let metrics = Array.from(metricSet);
              if (req?.prefix) {
                metrics = metrics.filter((m) => m.startsWith(req.prefix!));
              }
              return { metrics };
            },
          };
        })(),
        VectorBinding: {
          CreateNamespace: async () => ({}),
          DeleteNamespace: async () => {},
          ListNamespaces: async () => ({ namespaces: [] }),
          GetNamespaceInfo: async () => ({}),
          UpsertVector: async () => ({}),
          GetVector: async () => ({}),
          DeleteVector: async () => {},
          BatchUpsert: async () => ({}),
          BatchDeleteVectors: async () => ({}),
          ListVectors: async () => ({ vectors: [] }),
          UpdateMetadata: async () => {},
          SearchSimilar: async () => ({ results: [] }),
          SearchByMetadata: async () => ({ results: [] }),
          HybridSearch: async () => ({ results: [] }),
          SearchByID: async () => ({ results: [] }),
          RebuildIndex: async () => ({}),
          GetRebuildProgress: async () => ({}),
          GetIndexStats: async () => ({}),
          CompactNamespace: async () => ({}),
          GetCompactionProgress: async () => ({}),
        },
        UserBinding: {
          Create: async (req: { username: string; tenant?: string }) => {
            maybeReject("UserBinding.Create");
            const id = fakeUUID();
            const now = new Date().toISOString();
            const user = {
              id,
              username: req.username,
              tenant: req.tenant ?? "default",
              lifecycle: "active",
              createdAt: now,
              updatedAt: now,
            };
            userStore.set(id, user);
            return { ...user };
          },
          Get: async (id: string) => {
            maybeReject("UserBinding.Get");
            const user = userStore.get(id);
            if (!user) throw new Error(`user "${id}" not found`);
            return { ...user };
          },
          Update: async (req: { id: string; username?: string; tenant?: string }) => {
            maybeReject("UserBinding.Update");
            const user = userStore.get(req.id);
            if (!user) throw new Error(`user "${req.id}" not found`);
            if (req.username !== undefined) user.username = req.username;
            if (req.tenant !== undefined) user.tenant = req.tenant;
            user.updatedAt = new Date().toISOString();
            return { ...user };
          },
          Delete: async (id: string) => {
            maybeReject("UserBinding.Delete");
            userStore.delete(id);
          },
          List: async () => {
            maybeReject("UserBinding.List");
            const users = Array.from(userStore.values());
            return { users, total: users.length };
          },
          Enable: async (id: string) => {
            maybeReject("UserBinding.Enable");
            const user = userStore.get(id);
            if (user) {
              user.lifecycle = "active";
              user.updatedAt = new Date().toISOString();
            }
          },
          Disable: async (id: string) => {
            maybeReject("UserBinding.Disable");
            const user = userStore.get(id);
            if (user) {
              user.lifecycle = "disabled";
              user.updatedAt = new Date().toISOString();
            }
          },
        },
        RoleBinding: {
          Create: async (req: { name: string; tenant?: string; permissions?: string[] }) => {
            maybeReject("RoleBinding.Create");
            const id = fakeUUID();
            const now = new Date().toISOString();
            const role = {
              id,
              name: req.name,
              tenant: req.tenant ?? "default",
              permissions: req.permissions ?? [],
              createdAt: now,
              updatedAt: now,
            };
            roleStore.set(id, role);
            return { ...role };
          },
          Get: async (id: string) => {
            maybeReject("RoleBinding.Get");
            const role = roleStore.get(id);
            if (!role) throw new Error(`role "${id}" not found`);
            return { ...role };
          },
          Update: async (req: { id: string; name?: string; tenant?: string; permissions?: string[] }) => {
            maybeReject("RoleBinding.Update");
            const role = roleStore.get(req.id);
            if (!role) throw new Error(`role "${req.id}" not found`);
            if (req.name !== undefined) role.name = req.name;
            if (req.tenant !== undefined) role.tenant = req.tenant;
            if (req.permissions !== undefined) role.permissions = req.permissions;
            role.updatedAt = new Date().toISOString();
            return { ...role };
          },
          Delete: async (id: string) => {
            maybeReject("RoleBinding.Delete");
            roleStore.delete(id);
          },
          List: async () => {
            maybeReject("RoleBinding.List");
            const roles = Array.from(roleStore.values());
            return { roles, total: roles.length };
          },
          Assign: async () => {
            maybeReject("RoleBinding.Assign");
          },
          Unassign: async () => {
            maybeReject("RoleBinding.Unassign");
          },
          ListPermissions: async () => {
            maybeReject("RoleBinding.ListPermissions");
            return { categories: [] };
          },
        },
        CertBinding: {
          // Wire fields here (csrPEM, certPEM, keyPEM) are literal camelCase,
          // matching cmd/qrdb/bindings_certs.go's IssueCertInput/EnrollCertRequest
          // and sdk/go/transport/auth_types.go — NOT the snake_case Wails IPC
          // convention most other bindings use.
          Issue: async (req: { username: string; csrPEM?: string; ttl?: string }) => {
            maybeReject("CertBinding.Issue");
            const serial = String(++certSerialCounter);
            const now = new Date().toISOString();
            const expiresAt = new Date(Date.now() + 365 * 24 * 60 * 60 * 1000).toISOString();
            certStore.set(serial, {
              serial,
              username: req.username,
              tenantId: "default",
              issuedAt: now,
              expiresAt,
              revoked: false,
            });
            const out: Record<string, unknown> = {
              // userID crosses the wire as a string (json:"userID,string").
              userID: "1",
              username: req.username,
              tenantID: "default",
              certPEM: "-----BEGIN CERTIFICATE-----\nmock-cert-pem\n-----END CERTIFICATE-----",
              serial,
              thumbprint: "mock-thumbprint",
              notAfter: expiresAt,
            };
            // No CSR supplied: the (real) binding generates a key pair
            // LOCALLY and returns it alongside the certificate.
            if (!req.csrPEM) {
              out.keyPEM = "-----BEGIN PRIVATE KEY-----\nmock-key-pem\n-----END PRIVATE KEY-----";
            }
            return out;
          },
          Enroll: async (req: { username: string; certPEM: string; chainPEM?: string }) => {
            maybeReject("CertBinding.Enroll");
            const serial = String(++certSerialCounter);
            const now = new Date().toISOString();
            const expiresAt = new Date(Date.now() + 365 * 24 * 60 * 60 * 1000).toISOString();
            certStore.set(serial, {
              serial,
              username: req.username,
              tenantId: "default",
              issuedAt: now,
              expiresAt,
              revoked: false,
            });
            // Bring-your-own: the cluster did not issue it, so no serial.
            return {
              userID: "2",
              username: req.username,
              tenantID: "default",
              certPEM: req.certPEM,
              thumbprint: "mock-thumbprint",
              notAfter: expiresAt,
            };
          },
          Export: async (_req: { certPEM: string; keyPEM: string; passphrase: string }) => {
            maybeReject("CertBinding.Export");
            return { pkcs12: "bW9jay1wa2NzMTItZGF0YQ==" };
          },
          Revoke: async (serial: string) => {
            maybeReject("CertBinding.Revoke");
            const cert = certStore.get(serial);
            if (!cert) throw new Error(`certificate "${serial}" not found`);
            cert.revoked = true;
          },
          List: async (_tenant: string) => {
            maybeReject("CertBinding.List");
            return { certs: Array.from(certStore.values()) };
          },
        },
        CollectionBinding: {
          Create: async (req: { database: string; name: string; id_strategy?: string; field_hash?: string }) => {
            maybeReject("CollectionBinding.Create");
            const now = new Date().toISOString();
            const info = {
              name: req.name,
              database: req.database,
              id_strategy: req.id_strategy ?? "unix_nano",
              field_hash: req.field_hash,
              schema: {},
              document_count: 0,
              created_at: now,
            };
            collectionStore.set(`${req.database}/${req.name}`, info);
            if (!documentStore.has(`${req.database}/${req.name}`)) {
              documentStore.set(`${req.database}/${req.name}`, new Map());
            }
            return { ...info };
          },
          Get: async (database: string, name: string) => {
            maybeReject("CollectionBinding.Get");
            const key = `${database}/${name}`;
            const info = collectionStore.get(key);
            if (!info) throw new Error(`collection "${name}" not found`);
            // Recompute document_count from document store
            const docs = documentStore.get(key);
            const count = docs ? docs.size : 0;
            return { ...info, document_count: count };
          },
          List: async (database: string) => {
            maybeReject("CollectionBinding.List");
            const result: unknown[] = [];
            for (const [key, info] of collectionStore.entries()) {
              if (key.startsWith(`${database}/`)) {
                const docs = documentStore.get(key);
                const count = docs ? docs.size : 0;
                result.push({ ...info, document_count: count });
              }
            }
            return { collections: result };
          },
          Drop: async (database: string, name: string) => {
            maybeReject("CollectionBinding.Drop");
            const key = `${database}/${name}`;
            collectionStore.delete(key);
            documentStore.delete(key);
          },
          InsertDocument: async (req: { database: string; collection: string; document: Record<string, unknown> }) => {
            maybeReject("CollectionBinding.InsertDocument");
            const key = `${req.database}/${req.collection}`;
            if (!collectionStore.has(key)) throw new Error(`collection "${req.collection}" not found`);

            // Enforce unique index constraints before persisting the document.
            const indexBinding = (w.go as Record<string, unknown>)?.main as Record<string, unknown> | undefined;
            const ib = indexBinding?.IndexBinding as Record<string, unknown> | undefined;
            const liveIndexStore = ib?.__indexStore as Map<string, {
              name: string;
              fieldName: string;
              unique: boolean;
              sparse: boolean;
              numeric: boolean;
              entries: Map<string, string[]>;
            }> | undefined;

            if (liveIndexStore !== undefined) {
              const prefix = `${req.database}.${req.collection}.`;
              for (const [, idx] of liveIndexStore.entries()) {
                if (!idx.name.startsWith(prefix)) continue;
                if (!idx.unique) continue;
                const rawValue = req.document[idx.fieldName];
                if (rawValue === undefined || rawValue === null) continue;
                const fk = String(rawValue);
                if (idx.entries.has(fk)) {
                  throw new Error(
                    `unique constraint violation: index "${idx.name}" already contains value "${fk}"`
                  );
                }
              }
            }

            const id = nextDocId.toString();
            nextDocId = nextDocId + BigInt(1);
            let docs = documentStore.get(key);
            if (!docs) {
              docs = new Map();
              documentStore.set(key, docs);
            }
            docs.set(id, { ...req.document });

            // Update schema from document fields.
            const info = collectionStore.get(key)!;
            const schema = { ...(info.schema ?? {}) };
            for (const field of Object.keys(req.document)) {
              if (schema[field] === undefined) {
                const val = req.document[field];
                if (typeof val === "boolean") schema[field] = "BOOLEAN";
                else if (typeof val === "number") schema[field] = Number.isInteger(val) ? "BIGINT" : "DOUBLE";
                else if (typeof val === "object" && val !== null) schema[field] = "JSON";
                else schema[field] = "VARCHAR";
              }
            }
            collectionStore.set(key, { ...info, schema });

            // Live-index the new document into any matching indexes.
            const indexDoc = ib?.__indexDocument as
              | ((db: string, col: string, docId: string, doc: Record<string, unknown>) => void)
              | undefined;
            if (indexDoc !== undefined) {
              indexDoc(req.database, req.collection, id, req.document);
            }

            return { id };
          },
          GetDocument: async (database: string, collection: string, idStr: string) => {
            maybeReject("CollectionBinding.GetDocument");
            const key = `${database}/${collection}`;
            const docs = documentStore.get(key);
            if (!docs) throw new Error(`collection "${collection}" not found`);
            const doc = docs.get(idStr);
            if (!doc) throw new Error(`document "${idStr}" not found`);
            return { id: idStr, document: { ...doc } };
          },
          UpdateDocument: async (req: { database: string; collection: string; id: string; document: Record<string, unknown> }) => {
            maybeReject("CollectionBinding.UpdateDocument");
            const key = `${req.database}/${req.collection}`;
            const docs = documentStore.get(key);
            if (!docs) throw new Error(`collection "${req.collection}" not found`);
            if (!docs.has(req.id)) throw new Error(`document "${req.id}" not found`);
            docs.set(req.id, { ...req.document });
          },
          DeleteDocument: async (database: string, collection: string, idStr: string) => {
            maybeReject("CollectionBinding.DeleteDocument");
            const key = `${database}/${collection}`;
            const docs = documentStore.get(key);
            if (!docs) throw new Error(`collection "${collection}" not found`);
            docs.delete(idStr);
          },
          ListDocuments: async (req: { database: string; collection: string; page_size?: number; page_token?: string }) => {
            maybeReject("CollectionBinding.ListDocuments");
            const key = `${req.database}/${req.collection}`;
            const docs = documentStore.get(key);
            if (!docs) return { documents: [], next_page_token: "", total_count: 0 };
            const all: { id: string; document: Record<string, unknown> }[] = [];
            for (const [id, doc] of docs.entries()) {
              all.push({ id, document: { ...doc } });
            }
            return { documents: all, next_page_token: "", total_count: all.length };
          },
        },
        DatabaseBinding: {
          Create: async (req: {
            name: string;
            backend: string;
            protocol?: { mysql: boolean; postgresql: boolean };
            barrier?: { enabled: boolean; mode?: string; secret?: string; sharesRequired?: number; sharesTotal?: number };
          }) => {
            maybeReject("DatabaseBinding.Create");
            const s = getState();
            const db: Record<string, unknown> = {
              name: req.name,
              backend: req.backend || "pebble",
              status: "open",
            };
            // Mirrors DatabaseBarrierManager.EnsureBarrier for a Shamir
            // barrier, in its order: refused, with nothing created, when it
            // carries a secret (ShamirSecretNotAllowedError), on a node with
            // no share-holder custody (ShamirCustodyNotConfiguredError), or
            // with an M-of-N the seal cannot split (BarrierCreateError
            // wrapping seal.ErrShamirThresholdInvalid); otherwise registered
            // sealed at its M-of-N until its holders' custody is initialized.
            // No secret is stored.
            if (req.barrier?.enabled === true && req.barrier.mode === "shamir") {
              if (req.barrier.secret) {
                throw new Error(
                  `database: the shamir barrier of database "${req.name}" takes no secret: its key is split to its share holders`,
                );
              }
              if (s.databaseShamirCustody === false) {
                throw new Error(
                  `database: no share-holder custody is configured for the shamir barrier of database "${req.name}"`,
                );
              }
              const m = req.barrier.sharesRequired ?? 0;
              const n = req.barrier.sharesTotal ?? 0;
              if (m < 2 || m > n) {
                throw new Error(
                  `database: failed to create barrier for database "${req.name}": quicraft/seal: shamir threshold must be >= 2 and <= total shares`,
                );
              }
              db.barrier = { enabled: true, mode: "shamir", sharesRequired: m, sharesTotal: n };
              db.status = "sealed";
            }
            // Preserve protocol field so DatabaseInfo.protocol is populated.
            if (req.protocol !== undefined) {
              db.protocol = { ...req.protocol };
            }
            s.databases.push(db as unknown as typeof s.databases[0]);
            return db;
          },
          Get: async (name: string) => {
            maybeReject("DatabaseBinding.Get");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name);
            if (!db) throw new Error(`database "${name}" not found`);
            return db;
          },
          // Mirrors DatabaseService.DatabaseBarrierStatus through the desktop
          // binding: a database with no barrier is unknown to the barrier
          // registry, and a barrier never initialized carries the Go zero time.
          BarrierStatus: async (name: string) => {
            maybeReject("DatabaseBinding.BarrierStatus");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as
              | (Record<string, unknown> & { status?: string; barrier?: { enabled?: boolean; mode?: string } })
              | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            if (db.barrier?.enabled !== true) throw new Error("qrdb/seal: tenant not found");
            return {
              sealed: db.status === "sealed",
              strategy: db.barrier.mode === "shamir" ? "shamir" : "passphrase",
              hardware_backed: false,
              initialized_at: s.databaseBarrierInitializedAt?.[name] ?? "0001-01-01T00:00:00Z",
            };
          },
          List: async () => {
            maybeReject("DatabaseBinding.List");
            return getState().databases;
          },
          Drop: async (name: string) => {
            maybeReject("DatabaseBinding.Drop");
            const s = getState();
            s.databases = s.databases.filter((d: { name: string }) => d.name !== name);
          },
          // UpdateProtocol mirrors DatabaseBinding.UpdateProtocol: the Go
          // UpdateDatabaseProtocolRequest is {mysql, postgresql}.
          UpdateProtocol: async (name: string, protocol: { mysql: boolean; postgresql: boolean }) => {
            maybeReject("DatabaseBinding.UpdateProtocol");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            // Store under `protocol` to match SDK DatabaseInfo.protocol field.
            db.protocol = { ...protocol };
            return db;
          },
          // UpdateSQL mirrors DatabaseBinding.UpdateSQL: the Go
          // UpdateDatabaseSQLRequest is {sql: {concurrency}}; an absent sql
          // restores the defaults.
          UpdateSQL: async (name: string, req: { sql?: { concurrency?: string } }) => {
            maybeReject("DatabaseBinding.UpdateSQL");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            if (req.sql === undefined) {
              delete db.sql;
            } else {
              db.sql = { ...req.sql };
            }
            return db;
          },
          // UpdateCompaction mirrors DatabaseBinding.UpdateCompaction: the Go
          // UpdateDatabaseCompactionRequest is {compaction: {mode,
          // retainRevisions, retainPeriod}}; an absent compaction turns the
          // policy off.
          UpdateCompaction: async (
            name: string,
            req: { compaction?: { mode?: string; retainRevisions?: number; retainPeriod?: string } },
          ) => {
            maybeReject("DatabaseBinding.UpdateCompaction");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            if (req.compaction === undefined) {
              delete db.compaction;
            } else {
              db.compaction = { ...req.compaction };
            }
            return db;
          },
          UpdateRaft: async (name: string, raft: unknown) => {
            maybeReject("DatabaseBinding.UpdateRaft");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            db.raft = raft;
            return db;
          },
          UpdateBarrier: async (name: string, barrier: unknown) => {
            maybeReject("DatabaseBinding.UpdateBarrier");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            db.barrier = barrier;
            return db;
          },
          UpdateConnection: async (name: string, connection: unknown) => {
            maybeReject("DatabaseBinding.UpdateConnection");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            db.connection = connection;
            return db;
          },
          Scale: async (name: string, targetReplicas: number) => {
            maybeReject("DatabaseBinding.Scale");
            const s = getState();
            const db = s.databases.find((d: { name: string }) => d.name === name) as Record<string, unknown> | undefined;
            if (!db) throw new Error(`database "${name}" not found`);
            db.replicas = targetReplicas;
            return db;
          },
        },
        SQLBinding: {
          Execute: async (query: string, _database?: string) => {
            maybeReject("SQLBinding.Execute");
            const q = (query ?? "").trim();
            if (!q) {
              throw new Error("query is empty");
            }
            // Parse a handful of patterns used in e2e tests.
            const upper = q.toUpperCase();
            if (/^SELECT\s+1\s*$/.test(upper)) {
              return {
                columns: ["?column?"],
                rows: [{ "?column?": 1 }],
                rowCount: 1,
                duration: "0.5ms",
              };
            }
            if (/^SELECT\s+\*\s+FROM\s+KV\s*$/i.test(q)) {
              const kvRows: Record<string, string>[] = [];
              for (const [k, v] of kvStore.entries()) {
                kvRows.push({ key: k, value: v });
              }
              return {
                columns: ["key", "value"],
                rows: kvRows,
                rowCount: kvRows.length,
                duration: "1.2ms",
              };
            }
            if (/^SELECT\s+\*\s+FROM\s+DATAPOINTS\s*$/i.test(q)) {
              return {
                columns: ["metric", "value", "timestamp_ns"],
                rows: [],
                rowCount: 0,
                duration: "0.8ms",
              };
            }
            if (/^SELECT\s+\*\s+FROM\s+NAMESPACES\s*$/i.test(q)) {
              return {
                columns: ["name", "dimensions"],
                rows: [],
                rowCount: 0,
                duration: "0.6ms",
              };
            }
            // Anything else: simulate an invalid query error.
            throw new Error(`syntax error: unrecognized query: ${q}`);
          },
        },
        ConfigBinding: {
          Initialized: async () => {
            maybeReject("ConfigBinding.Initialized");
            return { initialized: getState().configInitialized };
          },
          IsEmbeddedAvailable: async () => {
            return true;
          },
        },
        ConnectionManagerBinding: (() => {
          // Track saved profiles so Connect can determine isEmbedded.
          const savedProfiles: Record<string, Record<string, unknown>> = {};
          return {
            ListProfiles: async () => {
              const s = getState();
              if (s.configInitialized) {
                const profileType = s.profileType ?? "embedded";
                return [{
                  id: "mock-profile-1",
                  name: "Local Database",
                  type: profileType,
                  ...(profileType === "embedded"
                    ? { embedded: { dataDir: "/mock/data" } }
                    : {
                        external: {
                          transport: "unix",
                          address: "/mock/run/qrdbd.sock",
                        },
                      }),
                  serverMode: s.serverMode,
                  createdAt: "2026-01-01T00:00:00Z",
                }];
              }
              return [];
            },
            SaveProfile: async (profile: Record<string, unknown>) => {
              if (profile.id) savedProfiles[profile.id as string] = profile;
            },
            DeleteProfile: async (id: string) => {
              delete savedProfiles[id];
            },
            Connect: async (profileId: string) => {
              const s = getState();
              const profile = savedProfiles[profileId];
              const isEmbedded = profile?.type === "embedded";
              // Embedded profiles auto-initialize their in-process seal barrier
              // on Connect (see initEmbeddedBarrier in embedded_barrier.go) —
              // EXCEPT shamir-mode barriers, which the desktop SealBinding
              // reports as uninitialized until InitializeShamir runs (the
              // ConnectionManager wizard depends on this to drive the
              // share-free officer enrollment flow). Mirror that here.
              const embedded = profile?.embedded as
                | { sealConfig?: { mode?: string } }
                | undefined;
              const sealMode = embedded?.sealConfig?.mode ?? "none";
              if (isEmbedded && sealMode !== "shamir" && !s.sealStatus.initialized) {
                s.sealStatus.initialized = true;
                s.sealStatus.sealed = false;
                s.configInitialized = true;
              }
              if (isEmbedded && sealMode === "shamir" && !s.configInitialized) {
                // The profile exists and the server is running; only the
                // barrier awaits its shamir ceremony.
                s.configInitialized = true;
              }
              let status: string;
              if (!s.configInitialized) {
                status = "needs-init";
              } else if (s.sealStatus.initialized && s.sealStatus.sealed) {
                status = "sealed";
              } else {
                status = "connected";
              }
              return { profileId, status, isEmbedded };
            },
            Disconnect: async (_profileId: string) => {
              // no-op in mock
            },
            GetState: async (profileId: string) => {
              const s = getState();
              const profile = savedProfiles[profileId];
              const isEmbedded = profile?.type === "embedded";
              if (!s.configInitialized) {
                return { profileId, status: "disconnected", isEmbedded };
              }
              // If the server is initialized but sealed, report sealed so the
              // app routes to the unseal screen rather than the main app.
              if (s.sealStatus.initialized && s.sealStatus.sealed) {
                return { profileId, status: "sealed", isEmbedded };
              }
              return { profileId, status: "connected", isEmbedded };
            },
            SetActive: async (_profileId: string) => {
              // no-op in mock
            },
            GetActiveProfileID: async () => {
              const s = getState();
              return s.configInitialized ? "mock-profile-1" : "";
            },
            IsEmbeddedAvailable: async () => {
              return true;
            },
          };
        })(),
        TenantBinding: {
          Create: async (req: {
            id: string;
            name: string;
            isolation_type?: string;
            data_root?: string;
            barrier_auto_unseal?: boolean;
            admin_username: string;
            admin_csr_pem?: string;
            admin_cert_pem?: string;
            admin_chain_pem?: string;
            trust_anchors_pem?: string;
          }) => {
            maybeReject("TenantBinding.Create");
            // Record the raw request so tests can assert exactly what the
            // SDK submitted (mirrors the DatabaseBinding.Create capture
            // pattern used by the database wizard specs).
            (window as unknown as Record<string, unknown>).__tenantCreateReq = req;
            if (!req.admin_username) {
              throw new Error("admin_username is required");
            }
            if (!req.admin_csr_pem && !req.admin_cert_pem) {
              throw new Error("exactly one of admin_csr_pem or admin_cert_pem is required");
            }
            const s = getState();
            const now = new Date().toISOString();
            const info: Record<string, unknown> = {
              id: req.id,
              name: req.name,
              isolation_type: req.isolation_type ?? "prefix",
              quotas: null,
              created_at: now,
              updated_at: now,
            };
            if (req.data_root) {
              info.data_root = req.data_root;
            }
            s.tenants.push(info as unknown as MockTenantInfo);
            if (req.trust_anchors_pem) {
              s.trustAnchors ??= {};
              s.trustAnchors[req.id] = [
                {
                  fingerprint: `mock-fp-${req.id}`,
                  subject: "CN=Mock Root CA",
                  not_before: now,
                  not_after: new Date(Date.now() + 10 * 365 * 24 * 60 * 60 * 1000).toISOString(),
                  cert_pem: req.trust_anchors_pem,
                  added_at: now,
                },
              ];
            }
            return {
              tenant: { ...info },
              admin: {
                // user_id crosses the wire as a string
                // (json:"user_id,omitempty,string").
                user_id: req.admin_cert_pem ? "0" : "1",
                username: req.admin_username,
                cert_pem: req.admin_csr_pem
                  ? "-----BEGIN CERTIFICATE-----\nmock-admin-cert-pem\n-----END CERTIFICATE-----"
                  : (req.admin_cert_pem ?? ""),
                serial: req.admin_csr_pem ? "mock-admin-serial" : undefined,
                thumbprint: "mock-admin-thumbprint",
                awaits_barrier_init: false,
              },
            };
          },
          Get: async (tenantId: string) => {
            maybeReject("TenantBinding.Get");
            const s = getState();
            const t = s.tenants.find((t: { id: string }) => t.id === tenantId);
            if (!t) throw new Error(`tenant "${tenantId}" not found`);
            return { tenant: { ...t } };
          },
          List: async () => {
            maybeReject("TenantBinding.List");
            const s = getState();
            return { tenants: s.tenants.map((t: MockTenantInfo) => ({ ...t })) };
          },
          Delete: async (tenantId: string) => {
            maybeReject("TenantBinding.Delete");
            const s = getState();
            const idx = s.tenants.findIndex((t: { id: string }) => t.id === tenantId);
            if (idx === -1) throw new Error(`tenant "${tenantId}" not found`);
            s.tenants.splice(idx, 1);
          },
          BarrierInit: async (_req: unknown) => {
            maybeReject("TenantBinding.BarrierInit");
          },
          // TenantBinding.BarrierUnseal: credential barriers only, with the
          // passphrase base64 in `key` (the Go field is []byte).
          BarrierUnseal: async (req: { tenant_id: string; key: string }) => {
            maybeReject("TenantBinding.BarrierUnseal");
            const s = getState();
            const tb = s.tenantBarriers?.[req.tenant_id];
            if (!tb) throw new Error(`tenant "${req.tenant_id}" barrier not initialized`);
            if (tb.strategy === "shamir") {
              throw new Error("qrdb/seal: a Shamir barrier unseals through a quorum of its share holders");
            }
            if (tb.passphrase !== undefined && atob(req.key) !== tb.passphrase) {
              throw new Error("tenant barrier: decryption failed: invalid credentials");
            }
            tb.sealed = false;
          },
          BarrierStatus: async (tenantId: string) => {
            maybeReject("TenantBinding.BarrierStatus");
            const s = getState();
            const tb = s.tenantBarriers?.[tenantId];
            if (!tb) throw new Error(`tenant "${tenantId}" barrier not initialized`);
            return {
              tenant_id: tenantId,
              sealed: tb.sealed,
              initialized: tb.initialized,
              strategy: tb.strategy,
              hardware_backed: false,
              shamir_threshold: tb.shamir_threshold ?? 0,
              shamir_total: tb.shamir_total ?? 0,
            };
          },
          UpdateQuotas: async (req: { tenant_id: string; quotas: MockTenantInfo["quotas"] }) => {
            maybeReject("TenantBinding.UpdateQuotas");
            const s = getState();
            const t = s.tenants.find((t: { id: string }) => t.id === req.tenant_id);
            if (!t) throw new Error(`tenant "${req.tenant_id}" not found`);
            t.quotas = req.quotas ?? null;
            t.updated_at = new Date().toISOString();
            return { tenant: { ...t } };
          },
          UpdateEntitlements: async (req: { tenant_id: string; entitlements: MockTenantInfo["entitlements"] }) => {
            maybeReject("TenantBinding.UpdateEntitlements");
            const s = getState();
            const t = s.tenants.find((t: { id: string }) => t.id === req.tenant_id);
            if (!t) throw new Error(`tenant "${req.tenant_id}" not found`);
            t.entitlements = req.entitlements ?? null;
            t.updated_at = new Date().toISOString();
            return { tenant: { ...t } };
          },
          // cmd/qrdb/bindings_tenant.go AddTrustAnchors takes (tenantID,
          // certsPEM string) directly, like its ListTrustAnchors/
          // RemoveTrustAnchor siblings below.
          AddTrustAnchors: async (tenantId: string, certsPem: string) => {
            maybeReject("TenantBinding.AddTrustAnchors");
            const s = getState();
            const now = new Date().toISOString();
            s.trustAnchors ??= {};
            const existing = s.trustAnchors[tenantId] ?? [];
            s.trustAnchors[tenantId] = [
              ...existing,
              {
                fingerprint: `mock-fp-${existing.length + 1}`,
                subject: "CN=Mock CA",
                not_before: now,
                not_after: new Date(Date.now() + 10 * 365 * 24 * 60 * 60 * 1000).toISOString(),
                cert_pem: certsPem,
                added_at: now,
              },
            ];
            return { anchors: [...s.trustAnchors[tenantId]] };
          },
          ListTrustAnchors: async (tenantId: string) => {
            maybeReject("TenantBinding.ListTrustAnchors");
            const s = getState();
            return { anchors: [...(s.trustAnchors?.[tenantId] ?? [])] };
          },
          RemoveTrustAnchor: async (tenantId: string, fingerprint: string) => {
            maybeReject("TenantBinding.RemoveTrustAnchor");
            const s = getState();
            const anchors = s.trustAnchors?.[tenantId];
            if (!anchors) throw new Error(`tenant "${tenantId}" has no trust anchors`);
            const idx = anchors.findIndex((a) => a.fingerprint === fingerprint);
            if (idx === -1) throw new Error(`trust anchor "${fingerprint}" not found`);
            anchors.splice(idx, 1);
          },
        },
        BackupBinding: {
          Create: async (req: { destination: string; compression?: string; encryption?: string; key_provider_id?: string; key_ref?: string }) => {
            maybeReject("BackupBinding.Create");
            backupCounter += 1;
            const info = {
              id: `backup-${String(backupCounter).padStart(3, "0")}`,
              timestamp: new Date().toISOString(),
              size: 1024 * (backupCounter + 1),
              shard_count: 3,
              status: "completed",
              checksum: `deadbeef${backupCounter}`,
              destination: req.destination,
              compression: req.compression ?? "none",
              encryption: req.encryption ?? "none",
            };
            backupStore.set(info.id, info);
            return { ...info };
          },
          List: async () => {
            maybeReject("BackupBinding.List");
            return { backups: [...backupStore.values()] };
          },
          Get: async (id: string) => {
            maybeReject("BackupBinding.Get");
            const info = backupStore.get(id);
            if (!info) throw new Error(`backup "${id}" not found`);
            return { ...info };
          },
          Delete: async (id: string) => {
            maybeReject("BackupBinding.Delete");
            if (!backupStore.has(id)) throw new Error(`backup "${id}" not found`);
            backupStore.delete(id);
          },
          Restore: async (req: { backup_id: string; validate_first?: boolean; decryption_key_ref?: string }) => {
            maybeReject("BackupBinding.Restore");
            if (!backupStore.has(req.backup_id)) throw new Error(`backup "${req.backup_id}" not found`);
            return {
              backup_id: req.backup_id,
              shards_restored: 3,
              duration: "1.2s",
              errors: [],
            };
          },
          Verify: async (id: string) => {
            maybeReject("BackupBinding.Verify");
            const info = backupStore.get(id);
            if (!info) throw new Error(`backup "${id}" not found`);
            return {
              valid: true,
              checksum_match: true,
              expected_checksum: info.checksum,
              actual_checksum: info.checksum,
              errors: [],
            };
          },
        },
        DaemonBinding: {
          Start: async (configPath: string) => {
            maybeReject("DaemonBinding.Start");
            daemonState.running = true;
            daemonState.pid = 12345;
            daemonState.logFile = "/tmp/qrdb.log";
            daemonState.logs = [
              `[INFO] Starting daemon with config: ${configPath}`,
              "[INFO] Daemon started successfully",
              "[INFO] Listening on :63001",
            ];
          },
          Stop: async () => {
            maybeReject("DaemonBinding.Stop");
            daemonState.running = false;
            daemonState.pid = null;
            daemonState.logs.push("[INFO] Daemon stopped");
          },
          Status: async () => {
            maybeReject("DaemonBinding.Status");
            return {
              running: daemonState.running,
              pid: daemonState.pid,
              logFile: daemonState.logFile,
            };
          },
          Restart: async (configPath: string) => {
            maybeReject("DaemonBinding.Restart");
            daemonState.running = false;
            daemonState.pid = null;
            daemonState.running = true;
            daemonState.pid = 12346;
            daemonState.logFile = "/tmp/qrdb.log";
            daemonState.logs = [
              `[INFO] Restarting daemon with config: ${configPath}`,
              "[INFO] Daemon restarted successfully",
            ];
          },
          GetLogs: async (maxLines: number) => {
            maybeReject("DaemonBinding.GetLogs");
            const lines = daemonState.logs.slice(-maxLines);
            return lines.join("\n");
          },
        },
        LockBinding: {
          Acquire: async (req: { key: string; holder_id?: string; ttl: number; reentrant?: boolean }) => {
            maybeReject("LockBinding.Acquire");
            const existing = lockStore.get(req.key);
            if (existing && !(req.reentrant && existing.reentrant && existing.holderId === req.holder_id)) {
              throw new Error(`lock "${req.key}" is held by another owner`);
            }
            lockFencingCounter += 1;
            const info = {
              key: req.key,
              fencingToken: lockFencingCounter,
              leaseId: lockFencingCounter,
              holderId: req.holder_id ?? `holder-${lockFencingCounter}`,
              reentrant: req.reentrant ?? false,
              acquireCount: existing ? existing.acquireCount + 1 : 1,
              ttl: req.ttl,
              acquiredAt: Math.floor(Date.now() / 1000),
            };
            lockStore.set(req.key, info);
            return { ...info };
          },
          TryLock: async (req: { key: string; holder_id?: string; ttl: number; reentrant?: boolean }) => {
            maybeReject("LockBinding.TryLock");
            if (lockStore.has(req.key)) {
              throw new Error(`lock "${req.key}" is already held`);
            }
            lockFencingCounter += 1;
            const info = {
              key: req.key,
              fencingToken: lockFencingCounter,
              leaseId: lockFencingCounter,
              holderId: req.holder_id ?? `holder-${lockFencingCounter}`,
              reentrant: req.reentrant ?? false,
              acquireCount: 1,
              ttl: req.ttl,
              acquiredAt: Math.floor(Date.now() / 1000),
            };
            lockStore.set(req.key, info);
            return { ...info };
          },
          Release: async (req: { key: string; fencing_token: number }) => {
            maybeReject("LockBinding.Release");
            const existing = lockStore.get(req.key);
            if (!existing || existing.fencingToken !== req.fencing_token) {
              throw new Error(`fencing token mismatch for lock "${req.key}"`);
            }
            lockStore.delete(req.key);
          },
          Extend: async (req: { key: string; fencing_token: number }) => {
            maybeReject("LockBinding.Extend");
            const existing = lockStore.get(req.key);
            if (!existing || existing.fencingToken !== req.fencing_token) {
              throw new Error(`fencing token mismatch for lock "${req.key}"`);
            }
            existing.acquiredAt = Math.floor(Date.now() / 1000);
            return { ...existing };
          },
          Status: async (key: string) => {
            maybeReject("LockBinding.Status");
            const existing = lockStore.get(key);
            if (!existing) throw new Error(`lock "${key}" not found`);
            return { ...existing };
          },
          List: async (prefix?: string) => {
            maybeReject("LockBinding.List");
            const all = [...lockStore.values()];
            const filtered = prefix ? all.filter((l) => l.key.startsWith(prefix)) : all;
            return { locks: filtered };
          },
        },
        RebalanceBinding: {
          GetShardStats: async (shardId: number) => {
            maybeReject("RebalanceBinding.GetShardStats");
            return {
              shardId,
              keyCount: 1000,
              byteCount: 1024 * 1024,
              leaderId: 1,
              replicaCount: 3,
            };
          },
          CreatePlan: async (_req: unknown) => {
            maybeReject("RebalanceBinding.CreatePlan");
            return {
              moves: [
                { shardId: 1, fromNode: "node-2", toNode: "node-1", estimatedBytes: 2048 },
              ],
              estimatedDuration: "3.4s",
              currentImbalance: 0.42,
            };
          },
          Execute: async (plan: { moves: unknown[] }) => {
            maybeReject("RebalanceBinding.Execute");
            return {
              movesCompleted: plan.moves.length,
              movesFailed: 0,
              duration: "3.5s",
              finalImbalance: 0.05,
            };
          },
          GetImbalanceReport: async () => {
            maybeReject("RebalanceBinding.GetImbalanceReport");
            return {
              nodes: [
                { nodeId: "node-1", address: "127.0.0.1:9001", shardCount: 2, totalKeys: 500, totalBytes: 512000, leaderCount: 1 },
                { nodeId: "node-2", address: "127.0.0.1:9002", shardCount: 1, totalKeys: 1500, totalBytes: 1536000, leaderCount: 2 },
              ],
              imbalanceScore: 0.42,
              hotspots: ["127.0.0.1:9002"],
            };
          },
        },
        CredentialBinding: {
          Submit: async (name: string, value: string) => {
            maybeReject("CredentialBinding.Submit");
            const history = credentialStore.get(name) ?? [];
            history.forEach((v) => { v.isCurrent = false; });
            history.push({ version: history.length + 1, value, isCurrent: true, createdAtUnix: Math.floor(Date.now() / 1000) });
            credentialStore.set(name, history);
          },
          Store: async (name: string, value: string) => {
            maybeReject("CredentialBinding.Store");
            const history = credentialStore.get(name) ?? [];
            history.forEach((v) => { v.isCurrent = false; });
            history.push({ version: history.length + 1, value, isCurrent: true, createdAtUnix: Math.floor(Date.now() / 1000) });
            credentialStore.set(name, history);
          },
          Retrieve: async (name: string) => {
            maybeReject("CredentialBinding.Retrieve");
            const history = credentialStore.get(name);
            const current = history?.find((v) => v.isCurrent);
            if (!current) throw new Error(`credential "${name}" not found`);
            return current.value;
          },
          List: async () => {
            maybeReject("CredentialBinding.List");
            return [...credentialStore.keys()].sort();
          },
          Strategy: async () => {
            maybeReject("CredentialBinding.Strategy");
            return "manual";
          },
          Delete: async (name: string) => {
            maybeReject("CredentialBinding.Delete");
            if (!credentialStore.has(name)) throw new Error(`credential "${name}" not found`);
            credentialStore.delete(name);
          },
          Rotate: async (req: { name: string; new_value: string; grace_period_seconds?: number }) => {
            maybeReject("CredentialBinding.Rotate");
            const history = credentialStore.get(req.name);
            if (!history || history.length === 0) throw new Error(`credential "${req.name}" not found`);
            const previous = history.find((v) => v.isCurrent);
            const previousVersion = previous?.version ?? 0;
            if (previous) {
              previous.isCurrent = false;
              if (req.grace_period_seconds && req.grace_period_seconds > 0) {
                previous.graceUntilUnix = Math.floor(Date.now() / 1000) + req.grace_period_seconds;
              }
            }
            const newVersion = previousVersion + 1;
            history.push({ version: newVersion, value: req.new_value, isCurrent: true, createdAtUnix: Math.floor(Date.now() / 1000) });
            return {
              name: req.name,
              previousVersion,
              newVersion,
              ...(req.grace_period_seconds ? { graceUntilUnix: Math.floor(Date.now() / 1000) + req.grace_period_seconds } : {}),
            };
          },
          ListVersions: async (name: string) => {
            maybeReject("CredentialBinding.ListVersions");
            const history = credentialStore.get(name) ?? [];
            return {
              name,
              versions: history.map((v) => ({
                version: v.version,
                isCurrent: v.isCurrent,
                createdAtUnix: v.createdAtUnix,
                ...(v.graceUntilUnix ? { graceUntilUnix: v.graceUntilUnix } : {}),
              })),
            };
          },
          RetrieveVersion: async (name: string, version: number) => {
            maybeReject("CredentialBinding.RetrieveVersion");
            const history = credentialStore.get(name) ?? [];
            const entry = version === 0 ? history.find((v) => v.isCurrent) : history.find((v) => v.version === version);
            if (!entry) throw new Error(`credential "${name}" version ${version} not found`);
            return entry.value;
          },
          GCExpired: async (name: string) => {
            maybeReject("CredentialBinding.GCExpired");
            const history = credentialStore.get(name);
            if (!history) throw new Error(`credential "${name}" not found`);
            const now = Math.floor(Date.now() / 1000);
            const before = history.length;
            const kept = history.filter((v) => v.isCurrent || !v.graceUntilUnix || v.graceUntilUnix > now);
            credentialStore.set(name, kept);
            return before - kept.length;
          },
        },
        AuditBinding: {
          Query: async (f: { actor?: string; resource?: string; action?: string; outcome?: string; tenant_id?: string; limit?: number }) => {
            maybeReject("AuditBinding.Query");
            const allEvents = [
              {
                timestamp: "2026-01-01T00:00:00Z",
                actor: "admin",
                resource: "barrier",
                action: "unseal",
                outcome: "success",
                requestID: "req-1",
              },
              {
                timestamp: "2026-01-02T00:00:00Z",
                actor: "alice",
                resource: "tenant:acme",
                action: "create",
                outcome: "success",
                tenantID: "acme",
                requestID: "req-2",
              },
            ];
            let events = allEvents;
            if (f?.actor) events = events.filter((e) => e.actor === f.actor);
            if (f?.resource) events = events.filter((e) => e.resource === f.resource);
            if (f?.action) events = events.filter((e) => e.action === f.action);
            if (f?.outcome) events = events.filter((e) => e.outcome.startsWith(f.outcome!));
            if (f?.tenant_id) events = events.filter((e) => e.tenantID === f.tenant_id);
            return { events, total: events.length };
          },
        },
      },
    };
  }, state);
}

/**
 * Extended test fixture that pre-injects Wails mock bindings.
 * Tests can override the initial mock state by passing it to the fixture.
 */
export const test = base.extend<{
  mockState: MockState;
}>({
  mockState: [defaultMockState, { option: true }],
  page: async ({ page, mockState }, use) => {
    // Auto-inject mock bindings before every test
    await injectWailsMock(page, mockState);
    await use(page);
  },
});

export { expect } from "@playwright/test";

/**
 * Helper to update the mock state at runtime (after page load).
 */
export async function updateMockState(
  page: Page,
  update: Partial<MockState>,
): Promise<void> {
  await page.evaluate((u: Partial<MockState>) => {
    const w = window as unknown as Record<string, unknown>;
    const current = w.__QRDB_MOCK_STATE__ as Record<string, unknown>;
    if (current) {
      Object.assign(current, u);
    }
  }, update);
}

/**
 * Helper to inject an error for a specific binding method.
 */
export async function injectError(
  page: Page,
  method: string,
  message: string,
): Promise<void> {
  await page.evaluate(
    ({ m, msg }: { m: string; msg: string }) => {
      const w = window as unknown as Record<string, unknown>;
      const state = w.__QRDB_MOCK_STATE__ as {
        errors: Record<string, string>;
      };
      state.errors[m] = msg;
    },
    { m: method, msg: message },
  );
}

/**
 * Helper to clear an injected error.
 */
export async function clearError(page: Page, method: string): Promise<void> {
  await page.evaluate((m: string) => {
    const w = window as unknown as Record<string, unknown>;
    const state = w.__QRDB_MOCK_STATE__ as {
      errors: Record<string, string>;
    };
    delete state.errors[m];
  }, method);
}

/**
 * Waits for the main app to finish loading after a second-launch (already
 * configured) scenario. With secondLaunchEmbeddedState the app auto-connects
 * to "Local Database" and navigates to the Dashboard, so we wait for the
 * Dashboard breadcrumb rather than the old "Select a Database" heading.
 */
export async function waitForAppReady(
  page: Page,
  timeout = 10_000,
): Promise<void> {
  await page.locator(".breadcrumbs").getByText("Dashboard").waitFor({
    state: "visible",
    timeout,
  });
}

/**
 * Navigates to the root URL and waits for the app to be ready.
 * Use this in place of the old pattern:
 *   await page.goto("/");
 *   await expect(page.getByRole("heading", { name: "Select a Database" })).toBeVisible(...)
 */
export async function gotoAndWaitForApp(
  page: Page,
  timeout = 10_000,
): Promise<void> {
  await page.goto("/");
  await waitForAppReady(page, timeout);
}

/**
 * Opens the Create Database wizard by clicking "New Database" in the sidebar.
 * The sidebar Databases section is always open, so the button is directly
 * accessible after the app loads.
 */
export async function openDatabaseWizard(
  page: Page,
  timeout = 10_000,
): Promise<void> {
  await page.locator("aside").getByText("New Database").click();
  await page.getByRole("heading", { name: "Database Name & Engine" }).waitFor({
    state: "visible",
    timeout,
  });
}
