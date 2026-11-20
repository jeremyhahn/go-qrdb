<!--
  EmbeddedServerPanel — desktop-only controls for the in-process embedded
  server: shows whether an embedded profile is connected (connection
  manager state) and whether a saved configuration exists, and offers Stop
  (disconnects the embedded profile), Restore saved config (connects the
  saved embedded profile) and Clear saved config. Starting a new embedded server goes through the embedded wizard
  (onStartNew); renders nothing when embedded mode is unavailable.
-->
<script lang="ts">
  import type { Client } from "@qrdb/sdk";
  import {
    connManagerStore,
    connectProfile,
    disconnectProfile,
    saveProfile,
  } from "$stores/connection-manager.svelte.ts";
  import type {
    ConnectionProfile,
    EmbeddedConfig,
  } from "$stores/connection-manager.svelte.ts";

  let {
    client,
    available,
    onStartNew,
  }: {
    client: Client;
    /** connectionManager.isEmbeddedAvailable() — false in web mode. */
    available: boolean;
    /** Opens the embedded server wizard (which owns startEmbedded). */
    onStartNew: () => void;
  } = $props();

  /**
   * The connected embedded profile, if any. The panel follows the profile
   * connection manager so a server started from a profile (or by Restore)
   * is registered there and shows as running. The connection manager starts
   * the embedded process before it probes the barrier, so a live server can
   * be sealed or awaiting init: those count as running too, and Stop
   * disconnects (stops) them.
   */
  const LIVE_EMBEDDED_STATUSES: ReadonlySet<string> = new Set([
    "connected",
    "sealed",
    "needs-init",
  ]);
  let runningProfileId: string | null = $derived(
    [...connManagerStore.states.values()].find(
      (s) => s.isEmbedded && LIVE_EMBEDDED_STATUSES.has(s.status),
    )?.profileId ?? null,
  );
  let running: boolean = $derived(runningProfileId !== null);
  let saved: boolean = $state(false);
  let busy: boolean = $state(false);
  let error: string = $state("");
  let confirmClear: boolean = $state(false);

  function message(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  async function refresh(): Promise<void> {
    try {
      saved = await client.embeddedServer.hasSavedConfig();
    } catch (err: unknown) {
      error = message(err);
    }
  }

  async function run(op: () => Promise<void>): Promise<void> {
    busy = true;
    error = "";
    try {
      await op();
    } catch (err: unknown) {
      error = message(err);
    } finally {
      busy = false;
      await refresh();
    }
  }

  function handleStop(): Promise<void> {
    const profileId = runningProfileId;
    if (profileId === null) return Promise.resolve();
    return run(() => disconnectProfile(client, profileId));
  }

  /**
   * A config with its empty members dropped (undefined, null, "", false,
   * empty objects) and its keys sorted, so a profile's embedded config and
   * the same config after the Go binding's JSON round trip (which fills
   * zero values) compare equal.
   */
  function normalizeConfig(value: unknown): unknown {
    if (value === null || value === undefined || value === "" || value === false) {
      return undefined;
    }
    if (Array.isArray(value)) {
      return value.map(normalizeConfig);
    }
    if (typeof value === "object") {
      const out: Record<string, unknown> = {};
      for (const key of Object.keys(value as Record<string, unknown>).sort()) {
        const v = normalizeConfig((value as Record<string, unknown>)[key]);
        if (v !== undefined) out[key] = v;
      }
      return Object.keys(out).length === 0 ? undefined : out;
    }
    return value;
  }

  function sameConfig(a: unknown, b: unknown): boolean {
    return JSON.stringify(normalizeConfig(a)) === JSON.stringify(normalizeConfig(b));
  }

  /**
   * The embedded profile whose config is the saved embedded configuration,
   * creating and saving one when no profile has it (a configuration saved
   * by a profile since deleted, or by a direct start).
   */
  async function profileForSavedConfig(): Promise<ConnectionProfile> {
    const saved = await client.embeddedServer.savedConfig();
    const match = connManagerStore.profiles.find(
      (p) => p.type === "embedded" && sameConfig(p.embedded, saved),
    );
    if (match !== undefined) return match;
    const profile: ConnectionProfile = {
      id: crypto.randomUUID(),
      name: saved.dataDir ? `Embedded server (${saved.dataDir})` : "Embedded server",
      type: "embedded",
      serverMode: saved.raftEnabled ? "raft" : "standalone",
      // The whole saved config, so the profile starts exactly what was saved.
      embedded: saved as unknown as EmbeddedConfig,
      createdAt: new Date().toISOString(),
    };
    await saveProfile(client, profile);
    return profile;
  }

  /**
   * Restore starts the saved embedded configuration through the connection
   * manager, so the started server is registered there as a managed
   * connection and the panel follows it.
   */
  function handleRestore(): Promise<void> {
    return run(async () => {
      const profile = await profileForSavedConfig();
      const state = await connectProfile(client, profile.id);
      if (state.status === "error") {
        throw new Error(state.error ?? "Embedded server failed to start");
      }
    });
  }

  async function handleClearConfirm(): Promise<void> {
    confirmClear = false;
    await run(() => client.embeddedServer.clearSavedConfig());
  }

  $effect(() => {
    if (available) {
      void refresh();
    }
  });
</script>

{#if available}
  <div class="card bg-base-100 mb-6 shadow-sm" data-testid="embedded-server-panel">
    <div class="card-body gap-3 p-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h3 class="font-semibold">Embedded server</h3>
        <div class="flex gap-2 text-xs">
          <span
            class="badge {running ? 'badge-success' : 'badge-ghost'}"
            data-testid="embedded-running"
          >
            {running ? "Running" : "Stopped"}
          </span>
          <span
            class="badge {saved ? 'badge-info' : 'badge-ghost'}"
            data-testid="embedded-saved"
          >
            {saved ? "Saved config" : "No saved config"}
          </span>
        </div>
      </div>

      {#if error}
        <div class="alert alert-error py-2 text-sm" role="alert" data-testid="embedded-error">
          {error}
        </div>
      {/if}

      <div class="flex flex-wrap gap-2">
        <button class="btn btn-sm btn-outline" onclick={onStartNew} disabled={busy}>
          Start new
        </button>
        <button
          class="btn btn-sm btn-outline"
          onclick={handleStop}
          disabled={busy || !running}
        >
          Stop
        </button>
        <button
          class="btn btn-sm btn-outline"
          onclick={handleRestore}
          disabled={busy || running}
        >
          Restore saved config
        </button>
        <button
          class="btn btn-sm btn-ghost text-error"
          onclick={() => (confirmClear = true)}
          disabled={busy || !saved}
        >
          Clear saved config
        </button>
      </div>

      {#if confirmClear}
        <div class="alert alert-warning py-2 text-sm" role="alertdialog" data-testid="embedded-clear-confirm">
          <span>Clear the saved embedded server configuration?</span>
          <div class="flex gap-2">
            <button class="btn btn-xs" onclick={() => (confirmClear = false)}>Cancel</button>
            <button class="btn btn-xs btn-error" onclick={handleClearConfirm}>Confirm clear</button>
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}
