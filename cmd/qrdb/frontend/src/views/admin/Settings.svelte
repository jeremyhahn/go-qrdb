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
<script lang="ts">
  import { onMount } from "svelte";
  import type { Client } from "@qrdb/sdk";
  import { Sun, Moon } from "lucide-svelte";
  import { themeStore, toggleTheme } from "$stores/theme.svelte.ts";
  import {
    connManagerStore,
    disconnectProfile,
  } from "$stores/connection-manager.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let versionInfo: Record<string, string> = $state({});
  let versionLoading: boolean = $state(true);

  /**
   * Transport status as reported by the SDK client (ConnectionClient.isConnected),
   * re-read after every action that can change it.
   */
  let clientConnected: boolean = $state(false);

  /**
   * Connected when the client transport is up and the active profile has not
   * been disconnected through the connection manager.
   */
  const isConnected = $derived(
    clientConnected &&
      connManagerStore.states.get(connManagerStore.activeProfileId)?.status !==
        "disconnected",
  );
  let disconnecting: boolean = $state(false);
  let disconnectError: string | null = $state(null);

  /** Server configuration (ServerConfigClient.initialized / isEmbeddedAvailable). */
  let serverInitialized: boolean | null = $state(null);
  let embeddedAvailable: boolean | null = $state(null);
  let serverLoading: boolean = $state(true);
  let serverError: string | null = $state(null);

  /**
   * Transport label derived from the active profile's external connection config,
   * or "embedded" for embedded profiles.
   */
  const transportLabel = $derived.by((): string => {
    const profile = connManagerStore.profiles.find(
      (p) => p.id === connManagerStore.activeProfileId,
    );
    if (profile === undefined) return "Unknown";
    if (profile.type === "embedded") return "embedded";
    return profile.external?.transport ?? "Unknown";
  });

  /**
   * Server address derived from the active profile's external connection config,
   * or "(embedded)" for embedded profiles.
   */
  const addressLabel = $derived.by((): string => {
    const profile = connManagerStore.profiles.find(
      (p) => p.id === connManagerStore.activeProfileId,
    );
    if (profile === undefined) return "Not connected";
    if (profile.type === "embedded") return "(embedded)";
    return profile.external?.address ?? "Not connected";
  });

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches version metadata from the client connection. */
  async function loadVersionInfo(): Promise<void> {
    try {
      versionLoading = true;
      const version = client.connection.getVersion();
      versionInfo = version as Record<string, string>;
    } catch {
      versionInfo = {};
    } finally {
      versionLoading = false;
    }
  }

  /** Reads the live connection status from the SDK client. */
  function refreshConnected(): void {
    try {
      clientConnected = client.connection.isConnected();
    } catch {
      clientConnected = false;
    }
  }

  /** Loads the server configuration state. */
  async function loadServerInfo(): Promise<void> {
    serverLoading = true;
    serverError = null;
    try {
      const [init, embedded] = await Promise.all([
        client.config.initialized(),
        client.config.isEmbeddedAvailable(),
      ]);
      serverInitialized = init.initialized;
      embeddedAvailable = embedded;
    } catch (err: unknown) {
      serverError = err instanceof Error ? err.message : String(err);
    } finally {
      serverLoading = false;
    }
  }

  /**
   * Closes the client connection, then disconnects the active profile through
   * the connection manager (disconnectProfile), which returns the app to the
   * connection manager.
   */
  async function handleDisconnect(): Promise<void> {
    disconnecting = true;
    disconnectError = null;
    try {
      await client.connection.disconnect();
      await disconnectProfile(client, connManagerStore.activeProfileId);
      refreshConnected();
    } catch (err: unknown) {
      disconnectError = err instanceof Error ? err.message : String(err);
      refreshConnected();
    } finally {
      disconnecting = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    refreshConnected();
    loadVersionInfo();
    loadServerInfo();
  });
</script>

<PageHeader
  title="Settings"
  description="Connection, appearance, and application information"
/>

<div class="flex flex-col gap-6">
  <!-- Connection section -->
  <div class="card bg-base-200 shadow">
    <div class="card-body">
      <h2 class="card-title text-lg">Connection</h2>

      <div class="stats bg-base-300 shadow-sm">
        <div class="stat">
          <div class="stat-title">Status</div>
          <div class="stat-value text-xl">
            <span
              class="badge badge-lg"
              data-testid="settings-connection-status"
              class:badge-success={isConnected}
              class:badge-error={!isConnected}
            >
              {isConnected ? "Connected" : "Disconnected"}
            </span>
          </div>
        </div>

        <div class="stat">
          <div class="stat-title">Transport</div>
          <div class="stat-value text-xl font-mono">{transportLabel}</div>
        </div>

        <div class="stat">
          <div class="stat-title">Address</div>
          <div class="stat-value text-sm font-mono">{addressLabel}</div>
        </div>
      </div>

      {#if connManagerStore.states.get(connManagerStore.activeProfileId)?.error !== undefined}
        <div class="alert alert-error mt-3">
          <span
            >{connManagerStore.states.get(connManagerStore.activeProfileId)
              ?.error}</span
          >
        </div>
      {/if}

      {#if disconnectError !== null}
        <div
          class="alert alert-error mt-3"
          data-testid="settings-disconnect-error"
        >
          <span>{disconnectError}</span>
        </div>
      {/if}

      <div class="card-actions mt-3 justify-end">
        <button
          type="button"
          class="btn btn-sm btn-outline btn-error"
          data-testid="settings-disconnect"
          disabled={!isConnected || disconnecting}
          onclick={handleDisconnect}
        >
          {#if disconnecting}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Disconnect
        </button>
      </div>
    </div>
  </div>

  <!-- Server section -->
  <div class="card bg-base-200 shadow" data-testid="settings-server">
    <div class="card-body">
      <h2 class="card-title text-lg">Server</h2>

      {#if serverLoading}
        <div class="flex justify-center py-4">
          <span class="loading loading-spinner loading-sm text-primary"></span>
        </div>
      {:else if serverError !== null}
        <div class="alert alert-error" data-testid="settings-server-error">
          <span>{serverError}</span>
        </div>
      {:else}
        <div class="stats bg-base-300 shadow-sm">
          <div class="stat">
            <div class="stat-title">Initialized</div>
            <div
              class="stat-value text-xl"
              data-testid="settings-server-initialized"
            >
              {serverInitialized ? "Yes" : "No"}
            </div>
          </div>
          <div class="stat">
            <div class="stat-title">Embedded server available</div>
            <div
              class="stat-value text-xl"
              data-testid="settings-server-embedded"
            >
              {embeddedAvailable ? "Yes" : "No"}
            </div>
          </div>
        </div>
      {/if}
    </div>
  </div>

  <!-- Theme section -->
  <div class="card bg-base-200 shadow">
    <div class="card-body">
      <h2 class="card-title text-lg">Theme</h2>
      <p class="text-base-content/70 text-sm">
        Toggle between light and dark mode. Your preference is persisted to
        local storage.
      </p>

      <div class="mt-3 flex items-center gap-4">
        <label class="swap swap-rotate">
          <input
            type="checkbox"
            checked={themeStore.theme === "dark"}
            onchange={toggleTheme}
            aria-label="Toggle dark mode"
          />

          <!-- Sun icon (light mode) -->
          <Sun class="swap-off h-8 w-8" />

          <!-- Moon icon (dark mode) -->
          <Moon class="swap-on h-8 w-8" />
        </label>

        <span class="text-sm capitalize">{themeStore.theme} mode</span>
      </div>
    </div>
  </div>

  <!-- About section -->
  <div class="card bg-base-200 shadow">
    <div class="card-body">
      <h2 class="card-title text-lg">About</h2>

      {#if versionLoading}
        <div class="flex justify-center py-4">
          <span class="loading loading-spinner loading-sm text-primary"></span>
        </div>
      {:else if Object.keys(versionInfo).length === 0}
        <p class="text-base-content/50 text-sm">
          Version information is not available.
        </p>
      {:else}
        <div class="overflow-x-auto">
          <table class="table table-sm">
            <thead>
              <tr>
                <th>Property</th>
                <th>Value</th>
              </tr>
            </thead>
            <tbody>
              {#each Object.entries(versionInfo) as [key, value] (key)}
                <tr>
                  <td class="font-semibold text-sm">{key}</td>
                  <td class="font-mono text-sm">{value}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <div class="divider text-xs">QRDB</div>
      <p class="text-base-content/60 text-xs">
        QuicRaft DB -- High-performance distributed key-value store powered by
        multi-group Raft consensus with QUIC transport.
      </p>
    </div>
  </div>
</div>
