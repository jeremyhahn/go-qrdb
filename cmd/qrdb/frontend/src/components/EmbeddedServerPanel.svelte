<!--
  EmbeddedServerPanel — desktop-only controls for the in-process embedded
  server (EmbeddedServerClient): shows whether it runs and whether a saved
  configuration exists, and offers Stop, Restore saved config and Clear saved
  config. Starting a new embedded server goes through the embedded wizard
  (onStartNew); renders nothing when embedded mode is unavailable.
-->
<script lang="ts">
  import type { Client } from "@qrdb/sdk";

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

  let running: boolean = $state(false);
  let saved: boolean = $state(false);
  let busy: boolean = $state(false);
  let error: string = $state("");
  let confirmClear: boolean = $state(false);

  function message(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  async function refresh(): Promise<void> {
    running = client.embeddedServer.isEmbedded();
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
    return run(() => client.embeddedServer.stopEmbedded());
  }

  function handleRestore(): Promise<void> {
    return run(() => client.embeddedServer.restoreEmbedded());
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
