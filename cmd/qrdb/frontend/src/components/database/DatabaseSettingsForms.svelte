<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->

<!--
  DatabaseSettingsForms renders the per-database settings forms: SQL
  concurrency (database.updateSQL), scheduled compaction
  (database.updateCompaction), Raft consensus (database.updateRaft), the
  encryption barrier (database.updateBarrier) and the remote connection
  (database.updateConnection). Each form validates its input, saves through
  the SDK call of the same name, and shows an inline success or error state.
  The `sections` prop picks which forms render: the admin detail view shows
  all five, the user settings view only the database-scoped tuning.
-->
<script lang="ts">
  import type {
    Client,
    CompactionMode,
    DatabaseBarrierConfig,
    DatabaseCompactionConfig,
    DatabaseConnectionConfig,
    DatabaseInfo,
    DatabaseRaftConfig,
    SQLConcurrency,
  } from "@qrdb/sdk";
  import { showToast } from "../../stores/toast.svelte.ts";

  export type SettingsSection =
    "sql" | "compaction" | "raft" | "barrier" | "connection";

  let {
    client,
    database,
    sections,
    onUpdated,
  }: {
    client: Client;
    database: DatabaseInfo;
    sections: readonly SettingsSection[];
    onUpdated: (db: DatabaseInfo) => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Validation helpers
  // ---------------------------------------------------------------------------

  /** A Go duration string: one or more number+unit terms, e.g. "1h30m". */
  const DURATION_PATTERN = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;
  const DURATION_TERM = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  const UNIT_SECONDS: Record<string, number> = {
    ns: 1e-9,
    us: 1e-6,
    µs: 1e-6,
    ms: 1e-3,
    s: 1,
    m: 60,
    h: 3600,
  };

  /** Seconds in a Go duration string, or null when it does not parse. */
  function durationSeconds(text: string): number | null {
    if (!DURATION_PATTERN.test(text)) return null;
    let total = 0;
    for (const m of text.matchAll(DURATION_TERM)) {
      total += Number(m[1]) * (UNIT_SECONDS[m[2] ?? ""] ?? 0);
    }
    return total;
  }

  /** A host:port (or [ipv6]:port) address with a port in 1..65535. */
  function isHostPort(text: string): boolean {
    const m = /^(\[[0-9a-fA-F:.]+\]|[^\s:[\]]+):(\d{1,5})$/.exec(text);
    if (m === null) return false;
    const port = Number(m[2]);
    return port >= 1 && port <= 65535;
  }

  /** Whether a number input holds a whole number of at least `min`. */
  function isIntAtLeast(
    v: number | null | undefined,
    min: number,
  ): v is number {
    return typeof v === "number" && Number.isInteger(v) && v >= min;
  }

  /** Whether a number input is empty. */
  function isBlank(v: number | null | undefined): boolean {
    return (
      v === null ||
      v === undefined ||
      (typeof v === "number" && Number.isNaN(v))
    );
  }

  /** Splits a peers list on commas, whitespace and newlines. */
  function splitList(text: string): string[] {
    return text
      .split(/[\s,]+/)
      .map((p) => p.trim())
      .filter((p) => p.length > 0);
  }

  function errorMessage(err: unknown, fallback: string): string {
    return err instanceof Error ? err.message : fallback;
  }

  // ---------------------------------------------------------------------------
  // Form state, loaded from the database whenever it changes identity
  // ---------------------------------------------------------------------------

  type Outcome = { kind: "success" | "error"; message: string } | null;

  // SQL
  let sqlConcurrency: "" | SQLConcurrency = $state("");
  let sqlSaving = $state(false);
  let sqlOutcome: Outcome = $state(null);

  // Compaction
  let compactionMode: CompactionMode = $state("off");
  let compactionRevisions: number | null = $state(null);
  let compactionPeriod: string = $state("");
  let compactionSaving = $state(false);
  let compactionOutcome: Outcome = $state(null);

  // Raft
  let raftClusterMode: string = $state("none");
  let raftReplicaId: number | null = $state(null);
  let raftAddress: string = $state("");
  let raftPeers: string = $state("");
  let raftLocalNodeCount: number | null = $state(null);
  let raftDiscovery: string = $state("");
  let raftSaving = $state(false);
  let raftOutcome: Outcome = $state(null);

  // Barrier
  let barrierEnabled: boolean = $state(false);
  let barrierMode: string = $state("passphrase");
  let barrierSharesRequired: number | null = $state(null);
  let barrierSharesTotal: number | null = $state(null);
  let barrierSaving = $state(false);
  let barrierOutcome: Outcome = $state(null);

  // Connection
  let connTransport: string = $state("grpc");
  let connAddress: string = $state("");
  let connCaFile: string = $state("");
  let connCertFile: string = $state("");
  let connKeyFile: string = $state("");
  let connSaving = $state(false);
  let connOutcome: Outcome = $state(null);

  /** Copies a database's current settings into the form fields. */
  function loadForms(db: DatabaseInfo): void {
    sqlConcurrency = db.sql?.concurrency ?? "";
    compactionMode = db.compaction?.mode ?? "off";
    compactionRevisions = db.compaction?.retainRevisions ?? null;
    compactionPeriod = db.compaction?.retainPeriod ?? "";
    raftClusterMode = db.raft?.clusterMode || "none";
    raftReplicaId = db.raft?.replicaId ?? null;
    raftAddress = db.raft?.raftAddress ?? "";
    raftPeers = (db.raft?.peers ?? []).join("\n");
    raftLocalNodeCount = db.raft?.localNodeCount ?? null;
    raftDiscovery = db.raft?.discovery ?? "";
    barrierEnabled = db.barrier?.enabled ?? false;
    barrierMode = db.barrier?.mode || "passphrase";
    barrierSharesRequired = db.barrier?.sharesRequired ?? null;
    barrierSharesTotal = db.barrier?.sharesTotal ?? null;
    connTransport = db.connection?.transport || "grpc";
    connAddress = db.connection?.address ?? "";
    connCaFile = db.connection?.caFile ?? "";
    connCertFile = db.connection?.certFile ?? "";
    connKeyFile = db.connection?.keyFile ?? "";
    sqlOutcome =
      compactionOutcome =
      raftOutcome =
      barrierOutcome =
      connOutcome =
        null;
  }

  // Plain (non-reactive) marker: reload the fields only when the database
  // switches, not on every snapshot a save returns.
  let loadedFor: string | null = null;
  $effect(() => {
    if (database.name !== loadedFor) {
      loadedFor = database.name;
      loadForms(database);
    }
  });

  // ---------------------------------------------------------------------------
  // Validation
  // ---------------------------------------------------------------------------

  let compactionError = $derived.by((): string => {
    if (
      compactionMode === "revision" &&
      !isIntAtLeast(compactionRevisions, 1)
    ) {
      return "Revisions to keep must be a whole number of at least 1.";
    }
    if (compactionMode === "period") {
      const secs = durationSeconds(compactionPeriod.trim());
      if (secs === null)
        return 'Retention period must be a duration such as "24h" or "90m".';
      if (secs < 1) return "Retention period must be at least 1s.";
    }
    return "";
  });

  let raftError = $derived.by((): string => {
    if (!isBlank(raftReplicaId) && !isIntAtLeast(raftReplicaId, 1)) {
      return "Replica ID must be a whole number of at least 1.";
    }
    const addr = raftAddress.trim();
    if (raftClusterMode === "distributed" && addr === "") {
      return "A distributed cluster needs a Raft address.";
    }
    if (addr !== "" && !isHostPort(addr)) {
      return "Raft address must be host:port.";
    }
    const badPeer = splitList(raftPeers).find((p) => !isHostPort(p));
    if (badPeer !== undefined) return `Peer "${badPeer}" must be host:port.`;
    if (!isBlank(raftLocalNodeCount) && !isIntAtLeast(raftLocalNodeCount, 1)) {
      return "Local node count must be a whole number of at least 1.";
    }
    return "";
  });

  let barrierError = $derived.by((): string => {
    if (!barrierEnabled || barrierMode !== "shamir") return "";
    if (!isIntAtLeast(barrierSharesTotal, 2)) {
      return "Total shares must be a whole number of at least 2.";
    }
    if (
      !isIntAtLeast(barrierSharesRequired, 1) ||
      barrierSharesRequired > barrierSharesTotal
    ) {
      return "Required shares must be between 1 and the total shares.";
    }
    return "";
  });

  let connError = $derived.by((): string => {
    const addr = connAddress.trim();
    if (addr === "") return "Address is required.";
    if (connTransport !== "unix" && !isHostPort(addr)) {
      return "Address must be host:port.";
    }
    if ((connCertFile.trim() === "") !== (connKeyFile.trim() === "")) {
      return "Client certificate and key files must be set together.";
    }
    return "";
  });

  // ---------------------------------------------------------------------------
  // Save actions
  // ---------------------------------------------------------------------------

  async function saveSQL(): Promise<void> {
    sqlOutcome = null;
    try {
      sqlSaving = true;
      const db =
        sqlConcurrency === ""
          ? await client.database.updateSQL(database.name)
          : await client.database.updateSQL(database.name, {
              concurrency: sqlConcurrency,
            });
      onUpdated(db);
      sqlOutcome = { kind: "success", message: "SQL settings saved." };
      showToast("success", "SQL settings saved");
    } catch (err: unknown) {
      const msg = errorMessage(err, "Failed to update SQL settings");
      sqlOutcome = { kind: "error", message: msg };
      showToast("error", msg);
    } finally {
      sqlSaving = false;
    }
  }

  async function saveCompaction(): Promise<void> {
    if (compactionError !== "") return;
    compactionOutcome = null;
    let policy: DatabaseCompactionConfig | undefined;
    if (compactionMode === "revision") {
      policy = {
        mode: "revision",
        retainRevisions: compactionRevisions as number,
      };
    } else if (compactionMode === "period") {
      policy = { mode: "period", retainPeriod: compactionPeriod.trim() };
    }
    try {
      compactionSaving = true;
      const db =
        policy === undefined
          ? await client.database.updateCompaction(database.name)
          : await client.database.updateCompaction(database.name, policy);
      onUpdated(db);
      compactionOutcome = {
        kind: "success",
        message: "Compaction policy saved.",
      };
      showToast("success", "Compaction policy saved");
    } catch (err: unknown) {
      const msg = errorMessage(err, "Failed to update compaction policy");
      compactionOutcome = { kind: "error", message: msg };
      showToast("error", msg);
    } finally {
      compactionSaving = false;
    }
  }

  async function saveRaft(): Promise<void> {
    if (raftError !== "") return;
    raftOutcome = null;
    const peers = splitList(raftPeers);
    const raft: DatabaseRaftConfig = {
      clusterMode: raftClusterMode,
      ...(isBlank(raftReplicaId) ? {} : { replicaId: raftReplicaId as number }),
      ...(raftAddress.trim() === "" ? {} : { raftAddress: raftAddress.trim() }),
      ...(peers.length === 0 ? {} : { peers }),
      ...(isBlank(raftLocalNodeCount)
        ? {}
        : { localNodeCount: raftLocalNodeCount as number }),
      ...(raftDiscovery === "" ? {} : { discovery: raftDiscovery }),
    };
    try {
      raftSaving = true;
      onUpdated(await client.database.updateRaft(database.name, raft));
      raftOutcome = { kind: "success", message: "Raft configuration saved." };
      showToast("success", "Raft configuration saved");
    } catch (err: unknown) {
      const msg = errorMessage(err, "Failed to update Raft configuration");
      raftOutcome = { kind: "error", message: msg };
      showToast("error", msg);
    } finally {
      raftSaving = false;
    }
  }

  async function saveBarrier(): Promise<void> {
    if (barrierError !== "") return;
    barrierOutcome = null;
    let barrier: DatabaseBarrierConfig = { enabled: false };
    if (barrierEnabled && barrierMode === "shamir") {
      barrier = {
        enabled: true,
        mode: "shamir",
        sharesRequired: barrierSharesRequired as number,
        sharesTotal: barrierSharesTotal as number,
      };
    } else if (barrierEnabled) {
      barrier = { enabled: true, mode: barrierMode };
    }
    try {
      barrierSaving = true;
      onUpdated(await client.database.updateBarrier(database.name, barrier));
      barrierOutcome = {
        kind: "success",
        message: "Barrier configuration saved.",
      };
      showToast("success", "Barrier configuration saved");
    } catch (err: unknown) {
      const msg = errorMessage(err, "Failed to update barrier configuration");
      barrierOutcome = { kind: "error", message: msg };
      showToast("error", msg);
    } finally {
      barrierSaving = false;
    }
  }

  async function saveConnection(): Promise<void> {
    if (connError !== "") return;
    connOutcome = null;
    const connection: DatabaseConnectionConfig = {
      transport: connTransport,
      address: connAddress.trim(),
      ...(connCaFile.trim() === "" ? {} : { caFile: connCaFile.trim() }),
      ...(connCertFile.trim() === "" ? {} : { certFile: connCertFile.trim() }),
      ...(connKeyFile.trim() === "" ? {} : { keyFile: connKeyFile.trim() }),
    };
    try {
      connSaving = true;
      onUpdated(
        await client.database.updateConnection(database.name, connection),
      );
      connOutcome = {
        kind: "success",
        message: "Connection configuration saved.",
      };
      showToast("success", "Connection configuration saved");
    } catch (err: unknown) {
      const msg = errorMessage(
        err,
        "Failed to update connection configuration",
      );
      connOutcome = { kind: "error", message: msg };
      showToast("error", msg);
    } finally {
      connSaving = false;
    }
  }

  function submit(handler: () => Promise<void>): (e: SubmitEvent) => void {
    return (e: SubmitEvent) => {
      e.preventDefault();
      void handler();
    };
  }
</script>

{#snippet outcome(o: Outcome)}
  {#if o !== null}
    <div
      class="alert alert-sm py-2 text-sm {o.kind === 'success'
        ? 'alert-success'
        : 'alert-error'}"
      role={o.kind === "success" ? "status" : "alert"}
      data-testid="outcome-{o.kind}"
    >
      {o.message}
    </div>
  {/if}
{/snippet}

{#snippet fieldError(msg: string)}
  {#if msg !== ""}
    <p class="text-error text-xs" data-testid="validation-error">{msg}</p>
  {/if}
{/snippet}

{#snippet saveButton(saving: boolean, disabled: boolean, label: string)}
  <button
    type="submit"
    class="btn btn-primary btn-sm w-fit"
    disabled={saving || disabled}
  >
    {#if saving}
      <span class="loading loading-spinner loading-xs"></span>
    {/if}
    {label}
  </button>
{/snippet}

{#if sections.includes("sql")}
  <form
    class="card bg-base-200 shadow"
    data-testid="db-settings-sql"
    onsubmit={submit(saveSQL)}
  >
    <div class="card-body gap-3">
      <h3 class="card-title text-base">SQL Settings</h3>
      <p class="text-base-content/70 text-sm">
        The default concurrency mode of SQL transactions on this database.
      </p>
      <label class="form-control w-full max-w-xs">
        <span class="label-text text-xs">Concurrency</span>
        <select
          class="select select-bordered select-sm"
          bind:value={sqlConcurrency}
          disabled={sqlSaving}
        >
          <option value="">Default (optimistic)</option>
          <option value="optimistic">Optimistic</option>
          <option value="pessimistic">Pessimistic</option>
        </select>
      </label>
      {@render saveButton(sqlSaving, false, "Save SQL Settings")}
      {@render outcome(sqlOutcome)}
    </div>
  </form>
{/if}

{#if sections.includes("compaction")}
  <form
    class="card bg-base-200 shadow"
    data-testid="db-settings-compaction"
    onsubmit={submit(saveCompaction)}
  >
    <div class="card-body gap-3">
      <h3 class="card-title text-base">Scheduled Compaction</h3>
      <p class="text-base-content/70 text-sm">
        Compact this database's revision history automatically, by revision
        count or by age.
      </p>
      {#if database.compactionUnavailable}
        <p class="text-warning text-xs">
          Compaction is unavailable on this database's state machine.
        </p>
      {/if}
      <div class="flex flex-wrap items-end gap-3">
        <label class="form-control w-full max-w-[12rem]">
          <span class="label-text text-xs">Mode</span>
          <select
            class="select select-bordered select-sm"
            bind:value={compactionMode}
            disabled={compactionSaving}
          >
            <option value="off">Off</option>
            <option value="revision">Keep last N revisions</option>
            <option value="period">Keep history newer than</option>
          </select>
        </label>
        {#if compactionMode === "revision"}
          <label class="form-control w-full max-w-[10rem]">
            <span class="label-text text-xs">Revisions to keep</span>
            <input
              type="number"
              min="1"
              class="input input-bordered input-sm"
              aria-label="Revisions to keep"
              bind:value={compactionRevisions}
              disabled={compactionSaving}
            />
          </label>
        {:else if compactionMode === "period"}
          <label class="form-control w-full max-w-[10rem]">
            <span class="label-text text-xs">Retention period</span>
            <input
              type="text"
              class="input input-bordered input-sm font-mono"
              aria-label="Retention period"
              placeholder="24h"
              bind:value={compactionPeriod}
              disabled={compactionSaving}
            />
          </label>
        {/if}
      </div>
      {@render fieldError(compactionError)}
      {@render saveButton(
        compactionSaving,
        compactionError !== "",
        "Save Compaction Policy",
      )}
      {@render outcome(compactionOutcome)}
    </div>
  </form>
{/if}

{#if sections.includes("raft")}
  <form
    class="card bg-base-200 shadow"
    data-testid="db-settings-raft"
    onsubmit={submit(saveRaft)}
  >
    <div class="card-body gap-3">
      <h3 class="card-title text-base">Raft Consensus</h3>
      <p class="text-base-content/70 text-sm">
        How this database replicates: no consensus, a local multi-node group, or
        a distributed cluster.
      </p>
      <div class="grid grid-cols-1 gap-3 md:grid-cols-3">
        <label class="form-control">
          <span class="label-text text-xs">Cluster mode</span>
          <select
            class="select select-bordered select-sm"
            aria-label="Cluster mode"
            bind:value={raftClusterMode}
            disabled={raftSaving}
          >
            <option value="none">None</option>
            <option value="local">Local</option>
            <option value="distributed">Distributed</option>
          </select>
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Replica ID</span>
          <input
            type="number"
            min="1"
            class="input input-bordered input-sm"
            aria-label="Replica ID"
            bind:value={raftReplicaId}
            disabled={raftSaving}
          />
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Raft address</span>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            aria-label="Raft address"
            placeholder="10.0.0.1:7000"
            bind:value={raftAddress}
            disabled={raftSaving}
          />
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Local node count</span>
          <input
            type="number"
            min="1"
            class="input input-bordered input-sm"
            aria-label="Local node count"
            bind:value={raftLocalNodeCount}
            disabled={raftSaving}
          />
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Discovery</span>
          <select
            class="select select-bordered select-sm"
            aria-label="Discovery"
            bind:value={raftDiscovery}
            disabled={raftSaving}
          >
            <option value="">None</option>
            <option value="static">Static</option>
            <option value="multicast">Multicast</option>
            <option value="dns">DNS</option>
            <option value="token">Token</option>
          </select>
        </label>
        <label class="form-control md:col-span-3">
          <span class="label-text text-xs">Peers (host:port, one per line)</span
          >
          <textarea
            class="textarea textarea-bordered textarea-sm font-mono"
            rows="2"
            aria-label="Peers"
            bind:value={raftPeers}
            disabled={raftSaving}></textarea>
        </label>
      </div>
      {@render fieldError(raftError)}
      {@render saveButton(
        raftSaving,
        raftError !== "",
        "Save Raft Configuration",
      )}
      {@render outcome(raftOutcome)}
    </div>
  </form>
{/if}

{#if sections.includes("barrier")}
  <form
    class="card bg-base-200 shadow"
    data-testid="db-settings-barrier"
    onsubmit={submit(saveBarrier)}
  >
    <div class="card-body gap-3">
      <h3 class="card-title text-base">Encryption Barrier</h3>
      <p class="text-base-content/70 text-sm">
        Encrypt this database at rest under a passphrase or an M-of-N Shamir
        split.
      </p>
      <label class="flex cursor-pointer items-center gap-3">
        <input
          type="checkbox"
          class="toggle toggle-sm toggle-primary"
          aria-label="Barrier enabled"
          bind:checked={barrierEnabled}
          disabled={barrierSaving}
        />
        <span class="text-sm font-medium">Enabled</span>
      </label>
      {#if barrierEnabled}
        <div class="flex flex-wrap items-end gap-3">
          <label class="form-control w-full max-w-[12rem]">
            <span class="label-text text-xs">Mode</span>
            <select
              class="select select-bordered select-sm"
              aria-label="Barrier mode"
              bind:value={barrierMode}
              disabled={barrierSaving}
            >
              <option value="passphrase">Passphrase</option>
              <option value="shamir">Shamir</option>
            </select>
          </label>
          {#if barrierMode === "shamir"}
            <label class="form-control w-full max-w-[10rem]">
              <span class="label-text text-xs">Required shares (M)</span>
              <input
                type="number"
                min="1"
                class="input input-bordered input-sm"
                aria-label="Required shares"
                bind:value={barrierSharesRequired}
                disabled={barrierSaving}
              />
            </label>
            <label class="form-control w-full max-w-[10rem]">
              <span class="label-text text-xs">Total shares (N)</span>
              <input
                type="number"
                min="2"
                class="input input-bordered input-sm"
                aria-label="Total shares"
                bind:value={barrierSharesTotal}
                disabled={barrierSaving}
              />
            </label>
          {/if}
        </div>
      {/if}
      {@render fieldError(barrierError)}
      {@render saveButton(
        barrierSaving,
        barrierError !== "",
        "Save Barrier Configuration",
      )}
      {@render outcome(barrierOutcome)}
    </div>
  </form>
{/if}

{#if sections.includes("connection")}
  <form
    class="card bg-base-200 shadow"
    data-testid="db-settings-connection"
    onsubmit={submit(saveConnection)}
  >
    <div class="card-body gap-3">
      <h3 class="card-title text-base">Remote Connection</h3>
      <p class="text-base-content/70 text-sm">
        The server this database connects to and the TLS files it presents.
      </p>
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
        <label class="form-control">
          <span class="label-text text-xs">Transport</span>
          <select
            class="select select-bordered select-sm"
            aria-label="Transport"
            bind:value={connTransport}
            disabled={connSaving}
          >
            <option value="grpc">gRPC</option>
            <option value="rest">REST</option>
            <option value="quic">QUIC</option>
            <option value="unix">Unix socket</option>
          </select>
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Address</span>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            aria-label="Address"
            placeholder={connTransport === "unix"
              ? "/run/qrdb/qrdb.sock"
              : "db.example.com:9090"}
            bind:value={connAddress}
            disabled={connSaving}
          />
        </label>
        <label class="form-control">
          <span class="label-text text-xs">CA file</span>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            aria-label="CA file"
            bind:value={connCaFile}
            disabled={connSaving}
          />
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Client certificate file</span>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            aria-label="Client certificate file"
            bind:value={connCertFile}
            disabled={connSaving}
          />
        </label>
        <label class="form-control">
          <span class="label-text text-xs">Client key file</span>
          <input
            type="text"
            class="input input-bordered input-sm font-mono"
            aria-label="Client key file"
            bind:value={connKeyFile}
            disabled={connSaving}
          />
        </label>
      </div>
      {@render fieldError(connError)}
      {@render saveButton(connSaving, connError !== "", "Save Connection")}
      {@render outcome(connOutcome)}
    </div>
  </form>
{/if}
