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
  import type { Client } from "@qrdb/sdk";
  import {
    Database,
    Plus,
    Server,
    Trash2,
    PlugZap,
    Unplug,
    CheckCircle2,
    AlertCircle,
    Clock,
    Lock,
    ChevronRight,
    Copy,
    Check,
  } from "lucide-svelte";
  import {
    connManagerStore,
    connectProfile,
    disconnectProfile,
    deleteProfile,
    setActiveProfile,
  } from "$stores/connection-manager.svelte.ts";
  import type {
    ConnectionProfile,
    ConnectionState,
    EmbeddedConfig,
    ExternalConnectionConfig,
  } from "$stores/connection-manager.svelte.ts";
  import StepIndicator from "./wizard/StepIndicator.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    client,
    onConnect,
  }: {
    client: Client;
    onConnect: (state: ConnectionState) => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Panel state
  // ---------------------------------------------------------------------------

  type ActivePanel = "none" | "embedded" | "external";
  let activePanel: ActivePanel = $state("none");

  // ---------------------------------------------------------------------------
  // Embedded wizard step types and state
  // ---------------------------------------------------------------------------

  type EmbeddedWizardStep =
    | "name"
    | "mode"
    | "discovery"
    | "barrier"
    | "summary"
    | "applying";

  const embeddedWizardSteps: readonly EmbeddedWizardStep[] = [
    "name",
    "mode",
    "discovery",
    "barrier",
    "summary",
    "applying",
  ] as const;

  /** Step labels shown in the step indicator (discovery is conditional). */
  const stepLabels: readonly string[] = [
    "Name",
    "Mode",
    "Discovery",
    "Barrier",
    "Summary",
    "Applying",
  ] as const;

  let embeddedStep: EmbeddedWizardStep = $state("name");

  /** Index of the current step in the full label array (always show all steps). */
  let embeddedStepIndex = $derived(embeddedWizardSteps.indexOf(embeddedStep));

  // Name + data dir step
  let embeddedName: string = $state("Local Server");
  let embeddedDataDir: string = $state("");

  // Server mode step
  let embeddedServerMode: "standalone" | "raft" = $state("standalone");

  // Discovery step (raft only)
  let embeddedDiscovery: "multicast" | "dns" | "static" = $state("multicast");
  let embeddedMulticastSecret: string = $state("");
  let embeddedDnsDomain: string = $state("");
  let embeddedStaticPeers: string = $state(""); // newline-separated

  // Barrier step
  let embeddedBarrierMode: "none" | "passphrase" | "shamir" = $state("passphrase");
  let embeddedPassphrase: string = $state("");
  let embeddedPassphraseConfirm: string = $state("");
  let embeddedShamirThreshold: number = $state(3);
  let embeddedShamirShares: number = $state(5);

  // Applying phase state
  interface ApplyPhase {
    readonly label: string;
    status: "pending" | "active" | "done" | "error";
  }

  let applyPhases: ApplyPhase[] = $state([]);
  let applyProgress: number = $state(0);
  let applyError: string = $state("");
  let applyCurrentLabel = $derived(
    applyPhases.find((p) => p.status === "active")?.label ??
      (applyError ? "Setup failed" : "Preparing..."),
  );

  // Shamir result state shown after applying (share-free: shares distributed via officer claim).
  let shamirShareCount: number = $state(0);
  let shamirThresholdResult: number = $state(0);
  let shamirRootToken: string = $state("");
  let copiedRootToken: boolean = $state(false);

  // Barrier validation
  let barrierValid = $derived.by(() => {
    if (embeddedBarrierMode === "passphrase") {
      return (
        embeddedPassphrase.length > 0 &&
        embeddedPassphrase === embeddedPassphraseConfirm
      );
    }
    if (embeddedBarrierMode === "shamir") {
      return (
        embeddedShamirThreshold > 0 &&
        embeddedShamirShares > 0 &&
        embeddedShamirThreshold <= embeddedShamirShares
      );
    }
    return true;
  });

  let barrierValidationError = $derived.by(() => {
    if (embeddedBarrierMode === "passphrase") {
      if (embeddedPassphrase.length === 0) return "Passphrase is required.";
      if (embeddedPassphrase !== embeddedPassphraseConfirm)
        return "Passphrases do not match.";
    }
    if (embeddedBarrierMode === "shamir") {
      if (embeddedShamirThreshold <= 0 || embeddedShamirShares <= 0)
        return "Both values must be greater than zero.";
      if (embeddedShamirThreshold > embeddedShamirShares)
        return "Shares required cannot exceed total shares.";
    }
    return null;
  });

  // ---------------------------------------------------------------------------
  // Add External form state
  // ---------------------------------------------------------------------------

  type Transport = "rest" | "grpc" | "quic" | "unix" | "mcp";

  const defaultAddresses: Readonly<Record<Transport, string>> = {
    rest: "https://localhost:63002",
    grpc: "localhost:63003",
    quic: "localhost:63004",
    unix: "/var/run/qrdbd/qrdbd.sock",
    mcp: "https://localhost:63005/mcp",
  } as const;

  let externalName: string = $state("Remote Server");
  let externalTransport: Transport = $state("grpc");
  let externalAddress: string = $state(defaultAddresses.grpc);
  let externalCaFile: string = $state("");
  let externalCertFile: string = $state("");
  let externalKeyFile: string = $state("");
  let externalSubmitting: boolean = $state(false);
  let externalError: string = $state("");

  // Keep address in sync when transport changes.
  $effect(() => {
    externalAddress = defaultAddresses[externalTransport];
  });

  // ---------------------------------------------------------------------------
  // Delete confirmation state
  // ---------------------------------------------------------------------------

  let pendingDeleteId: string | null = $state(null);

  // ---------------------------------------------------------------------------
  // Derived helpers
  // ---------------------------------------------------------------------------

  /** Returns the connection state for a profile, or a default disconnected state. */
  function stateFor(profileId: string): ConnectionState {
    return (
      connManagerStore.states.get(profileId) ?? {
        profileId,
        status: "disconnected",
        isEmbedded: false,
      }
    );
  }

  /** Maps a connection status to a DaisyUI badge class. */
  function badgeClass(status: ConnectionState["status"]): string {
    const classes: Record<ConnectionState["status"], string> = {
      disconnected: "badge-ghost",
      connecting: "badge-info",
      "needs-init": "badge-warning",
      sealed: "badge-warning",
      connected: "badge-success",
      error: "badge-error",
    };
    return classes[status];
  }

  /** Maps a connection status to a human-readable label. */
  function statusLabel(status: ConnectionState["status"]): string {
    const labels: Record<ConnectionState["status"], string> = {
      disconnected: "Disconnected",
      connecting: "Connecting",
      "needs-init": "Needs Init",
      sealed: "Sealed",
      connected: "Connected",
      error: "Error",
    };
    return labels[status];
  }

  /** Formats an ISO 8601 timestamp as a locale date string. */
  function formatDate(iso: string): string {
    return new Date(iso).toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  }

  /** Parsed static peers array from the textarea input. */
  function parsedStaticPeers(): string[] {
    return embeddedStaticPeers
      .split("\n")
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
  }

  // ---------------------------------------------------------------------------
  // Profile card handlers
  // ---------------------------------------------------------------------------

  async function handleConnect(profileId: string): Promise<void> {
    try {
      const state = await connectProfile(client, profileId);
      if (state.status === "connected") {
        await setActiveProfile(client, profileId);
        onConnect(state);
      } else {
        onConnect(state);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Connection failed";
      const errState: ConnectionState = {
        profileId,
        status: "error",
        error: msg,
        isEmbedded:
          connManagerStore.profiles.find((p) => p.id === profileId)?.type ===
          "embedded",
      };
      connManagerStore.states = new Map(connManagerStore.states).set(
        profileId,
        errState,
      );
    }
  }

  async function handleDisconnect(profileId: string): Promise<void> {
    try {
      await disconnectProfile(client, profileId);
    } catch (err: unknown) {
      console.error("[ConnectionManager] disconnect error", err);
    }
  }

  async function handleCardClick(profile: ConnectionProfile): Promise<void> {
    const state = stateFor(profile.id);
    if (state.status !== "connected") return;
    await setActiveProfile(client, profile.id);
    onConnect(state);
  }

  function confirmDelete(profileId: string): void {
    pendingDeleteId = profileId;
  }

  async function handleDeleteConfirm(): Promise<void> {
    if (pendingDeleteId === null) return;
    try {
      await deleteProfile(client, pendingDeleteId);
    } catch (err: unknown) {
      console.error("[ConnectionManager] delete error", err);
    } finally {
      pendingDeleteId = null;
    }
  }

  function handleDeleteCancel(): void {
    pendingDeleteId = null;
  }

  // ---------------------------------------------------------------------------
  // Embedded wizard navigation
  // ---------------------------------------------------------------------------

  function showEmbeddedPanel(): void {
    activePanel = activePanel === "embedded" ? "none" : "embedded";
    resetEmbeddedWizard();
  }

  function resetEmbeddedWizard(): void {
    embeddedStep = "name";
    embeddedName = "Local Server";
    embeddedDataDir = "";
    embeddedServerMode = "standalone";
    embeddedDiscovery = "multicast";
    embeddedMulticastSecret = "";
    embeddedDnsDomain = "";
    embeddedStaticPeers = "";
    embeddedBarrierMode = "passphrase";
    embeddedPassphrase = "";
    embeddedPassphraseConfirm = "";
    embeddedShamirThreshold = 3;
    embeddedShamirShares = 5;
    applyPhases = [];
    applyProgress = 0;
    applyError = "";
    shamirShareCount = 0;
    shamirThresholdResult = 0;
    shamirRootToken = "";
  }

  function wizardBack(): void {
    const steps = embeddedWizardSteps;
    const idx = steps.indexOf(embeddedStep);
    if (idx <= 0) return;

    // When going back from "barrier", skip "discovery" if standalone.
    if (embeddedStep === "barrier" && embeddedServerMode === "standalone") {
      embeddedStep = "mode";
      return;
    }

    const prev = steps[idx - 1];
    if (prev !== undefined) embeddedStep = prev;
  }

  function wizardNext(): void {
    const steps = embeddedWizardSteps;
    const idx = steps.indexOf(embeddedStep);
    if (idx >= steps.length - 1) return;

    // Skip "discovery" when standalone.
    if (embeddedStep === "mode" && embeddedServerMode === "standalone") {
      embeddedStep = "barrier";
      return;
    }

    const next = steps[idx + 1];
    if (next !== undefined) embeddedStep = next;
  }

  function startApplying(): void {
    embeddedStep = "applying";
    runApply();
  }

  // ---------------------------------------------------------------------------
  // Applying phase logic
  // ---------------------------------------------------------------------------

  function buildApplyPhases(): ApplyPhase[] {
    const phases: ApplyPhase[] = [
      { label: "Saving server configuration...", status: "pending" },
      { label: "Starting embedded server...", status: "pending" },
    ];
    if (embeddedBarrierMode !== "none") {
      phases.push({ label: "Initializing encryption barrier...", status: "pending" });
    }
    phases.push({ label: "Verifying server health...", status: "pending" });
    phases.push({ label: "Complete", status: "pending" });
    return phases;
  }

  function advanceApplyPhase(idx: number): void {
    if (applyPhases[idx] !== undefined) {
      applyPhases[idx] = { ...applyPhases[idx], status: "active" };
    }
    applyProgress = Math.round(((idx + 1) / applyPhases.length) * 100);
  }

  function completeApplyPhase(idx: number): void {
    if (applyPhases[idx] !== undefined) {
      applyPhases[idx] = { ...applyPhases[idx], status: "done" };
    }
  }

  function failApplyPhase(idx: number): void {
    if (applyPhases[idx] !== undefined) {
      applyPhases[idx] = { ...applyPhases[idx], status: "error" };
    }
  }

  async function runApply(): Promise<void> {
    applyPhases = buildApplyPhases();
    applyError = "";
    applyProgress = 0;

    let phaseIdx = 0;
    let profileId = "";

    try {
      // Phase 1: Save server configuration.
      advanceApplyPhase(phaseIdx);
      const peers =
        embeddedServerMode === "raft" ? parsedStaticPeers() : undefined;

      const serverName = embeddedName.trim() || "Local Server";
      // Generate a unique data directory per embedded server to prevent conflicts.
      const slug = serverName.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
      const dataDir = embeddedDataDir.trim() || `~/.config/qrdb/servers/${slug}`;

      const profile: ConnectionProfile = {
        id: crypto.randomUUID(),
        name: serverName,
        type: "embedded",
        serverMode: embeddedServerMode,
        embedded: {
          dataDir,
          raftEnabled: embeddedServerMode === "raft",
          ...(embeddedServerMode === "raft"
            ? {
                raftConfig: {
                  discovery: embeddedDiscovery,
                  ...(embeddedDiscovery === "multicast" && embeddedMulticastSecret
                    ? { multicastSecret: embeddedMulticastSecret }
                    : {}),
                  ...(embeddedDiscovery === "dns" && embeddedDnsDomain
                    ? { dnsDomain: embeddedDnsDomain }
                    : {}),
                  ...(embeddedDiscovery === "static" && peers !== undefined && peers.length > 0
                    ? { peers }
                    : {}),
                },
              }
            : {}),
          sealConfig: {
            mode: embeddedBarrierMode,
          },
        } satisfies EmbeddedConfig,
        createdAt: new Date().toISOString(),
      };

      await client.connectionManager.saveProfile(profile as never);
      connManagerStore.profiles = [...connManagerStore.profiles, profile];
      profileId = profile.id;
      completeApplyPhase(phaseIdx);
      phaseIdx++;

      // Phase 2: Start embedded server (connect).
      advanceApplyPhase(phaseIdx);
      const connState = await connectProfile(client, profileId);
      if (connState.status === "error") {
        throw new Error(connState.error ?? "Failed to start embedded server");
      }
      // Set this connection as active immediately so subsequent SDK calls
      // (seal.initialize, admin.status) route through the correct client.
      await setActiveProfile(client, profileId);
      completeApplyPhase(phaseIdx);
      phaseIdx++;

      // Phase 3 (conditional): Initialize encryption barrier.
      //
      // In embedded mode a "none"-strategy barrier is auto-initialized when
      // StartEmbedded runs (see initEmbeddedBarrier in
      // cmd/qrdb/embedded_barrier.go); passphrase and shamir barriers are
      // constructed but left uninitialized so credentials flow only from
      // the operator (INV-1). We check the barrier status first and skip
      // init when it is already done, keeping the wizard idempotent and
      // avoiding "already initialized" errors that would otherwise abort
      // the flow with a generic toast.
      const preStatus = await client.seal.status();
      const alreadyInitialized = preStatus.initialized;

      if (embeddedBarrierMode !== "none") {
        advanceApplyPhase(phaseIdx);
        if (alreadyInitialized) {
          // Barrier was auto-initialized by the embedded server; honor the
          // phase for UX continuity but do not call initialize again.
        } else if (embeddedBarrierMode === "shamir") {
          const result = await client.seal.initializeShamir({
            threshold: embeddedShamirThreshold,
            totalShares: embeddedShamirShares,
          });
          shamirShareCount = result.shareCount;
          shamirThresholdResult = result.threshold;
          shamirRootToken = result.rootToken;
        } else {
          await client.seal.initialize(embeddedPassphrase);
        }
        completeApplyPhase(phaseIdx);
        phaseIdx++;
      } else if (!alreadyInitialized) {
        // For "none" mode we still need to mark the barrier as initialized
        // when the embedded server did not already do so.
        await client.seal.initialize("");
      }

      // Phase 4: Verify server health.
      advanceApplyPhase(phaseIdx);
      await client.admin.status();
      completeApplyPhase(phaseIdx);
      phaseIdx++;

      // Phase 5: Complete.
      advanceApplyPhase(phaseIdx);
      completeApplyPhase(phaseIdx);
      applyProgress = 100;

      // Yield to Svelte reactivity before transitioning.
      await new Promise<void>((r) => setTimeout(r, 120));

      // If shamir, wait for user to acknowledge shares before routing.
      if (embeddedBarrierMode !== "shamir") {
        activePanel = "none";
        // Barrier is now initialized; update the store's cached state so
        // subsequent sidebar clicks see the connection as fully-connected.
        // Without this, the cached state would still be "needs-init" from
        // the initial connectProfile() call above (which happened before
        // seal.initialize), and clicking the server row would try to
        // reconnect — which the backend used to refuse as already active.
        const finalState: ConnectionState = {
          ...connState,
          status: "connected",
        };
        connManagerStore.states = new Map(connManagerStore.states).set(
          profileId,
          finalState,
        );
        onConnect(finalState);
      }
      // If shamir: the UI shows the officer enrollment panel. The "Continue to Server"
      // button is immediately enabled — shares are distributed via per-officer claim flow.
    } catch (err: unknown) {
      console.error("[ConnectionManager] embedded wizard apply error", err);
      if (phaseIdx >= 0 && phaseIdx < applyPhases.length) {
        failApplyPhase(phaseIdx);
      }
      applyError = err instanceof Error ? err.message : "An unknown error occurred";
      // If we got far enough to start the embedded server, tear it down so a
      // retry can succeed without hitting "connection already active".
      if (profileId !== "") {
        try {
          await disconnectProfile(client, profileId);
        } catch (stopErr: unknown) {
          console.warn(
            "[ConnectionManager] failed to stop orphaned embedded server",
            stopErr,
          );
        }
      }
    }
  }

  function handleShamirAck(): void {
    // Barrier initialized (share-free). Officers claim their shares independently
    // via the /officer/claim page. Transition to connected state immediately.
    const profile = connManagerStore.profiles[connManagerStore.profiles.length - 1];
    if (profile === undefined) return;
    activePanel = "none";
    const existing = connManagerStore.states.get(profile.id);
    const finalState: ConnectionState = {
      ...(existing ?? { profileId: profile.id, isEmbedded: true, status: "connected" }),
      profileId: profile.id,
      status: "connected",
      isEmbedded: true,
    };
    connManagerStore.states = new Map(connManagerStore.states).set(
      profile.id,
      finalState,
    );
    onConnect(finalState);
  }

  function handleRetryApply(): void {
    applyPhases = [];
    applyProgress = 0;
    applyError = "";
    shamirShareCount = 0;
    shamirThresholdResult = 0;
    shamirRootToken = "";
    runApply();
  }

  async function copyToClipboard(text: string): Promise<void> {
    await navigator.clipboard.writeText(text);
    copiedRootToken = true;
    setTimeout(() => {
      copiedRootToken = false;
    }, 2000);
  }

  // ---------------------------------------------------------------------------
  // External form handler
  // ---------------------------------------------------------------------------

  function showExternalPanel(): void {
    activePanel = activePanel === "external" ? "none" : "external";
    externalError = "";
  }

  async function handleAddExternal(): Promise<void> {
    externalError = "";
    externalSubmitting = true;
    try {
      const profile: ConnectionProfile = {
        id: crypto.randomUUID(),
        name: externalName.trim() || "Remote Server",
        type: "external",
        external: {
          transport: externalTransport,
          address: externalAddress.trim(),
          ...(externalCaFile.trim() ? { caFile: externalCaFile.trim() } : {}),
          ...(externalCertFile.trim() ? { certFile: externalCertFile.trim() } : {}),
          ...(externalKeyFile.trim() ? { keyFile: externalKeyFile.trim() } : {}),
        } satisfies ExternalConnectionConfig,
        createdAt: new Date().toISOString(),
      };
      await client.connectionManager.saveProfile(profile as never);
      connManagerStore.profiles = [...connManagerStore.profiles, profile];
      activePanel = "none";
      const state = await connectProfile(client, profile.id);
      if (
        state.status === "connected" ||
        state.status === "needs-init" ||
        state.status === "sealed"
      ) {
        if (state.status === "connected") {
          await setActiveProfile(client, profile.id);
        }
        onConnect(state);
      }
    } catch (err: unknown) {
      externalError =
        err instanceof Error ? err.message : "Failed to connect to server";
    } finally {
      externalSubmitting = false;
    }
  }
</script>

<div class="flex min-h-screen flex-col">
  <!-- Header -->
  <header class="border-base-300 bg-base-100 border-b px-6 py-4">
    <div class="flex items-center gap-3">
      <Database class="text-primary h-7 w-7" strokeWidth={2} />
      <div>
        <h1 class="text-xl font-bold leading-none">QRDB</h1>
        <p class="text-base-content/60 text-xs">Connection Manager</p>
      </div>
    </div>
  </header>

  <!-- Main content -->
  <main class="flex-1 overflow-auto p-6">
    <!-- Loading state -->
    {#if connManagerStore.loading}
      <div class="flex items-center justify-center py-20">
        <span class="loading loading-spinner loading-lg text-primary"></span>
      </div>

    <!-- Empty state -->
    {:else if connManagerStore.profiles.length === 0 && activePanel === "none"}
      <div class="flex flex-col items-center justify-center py-20">
        <div class="card bg-base-100 w-full max-w-md shadow-xl">
          <div class="card-body items-center text-center gap-4">
            <Database class="text-base-content/30 h-16 w-16" strokeWidth={1} />
            <div>
              <h2 class="card-title justify-center">No servers configured</h2>
              <p class="text-base-content/60 mt-1 text-sm">
                Create an embedded server or connect to a remote QRDB daemon to get started.
              </p>
            </div>
            <div class="card-actions flex-col w-full gap-2 mt-2">
              {#if connManagerStore.embeddedAvailable}
                <button
                  class="btn btn-primary btn-block"
                  onclick={showEmbeddedPanel}
                >
                  <Database class="h-4 w-4" />
                  Create Embedded Server
                </button>
              {/if}
              <button
                class="btn btn-outline btn-block"
                onclick={showExternalPanel}
              >
                <Server class="h-4 w-4" />
                Connect to Server
              </button>
            </div>
          </div>
        </div>
      </div>

    <!-- Profile grid -->
    {:else if connManagerStore.profiles.length > 0}
      <div class="mb-6 flex items-center justify-between">
        <h2 class="text-lg font-semibold">Servers</h2>
        <div class="flex gap-2">
          {#if connManagerStore.embeddedAvailable}
            <button
              class="btn btn-sm btn-outline"
              onclick={showEmbeddedPanel}
              aria-pressed={activePanel === "embedded"}
            >
              <Plus class="h-3.5 w-3.5" />
              Embedded
            </button>
          {/if}
          <button
            class="btn btn-sm btn-primary"
            onclick={showExternalPanel}
            aria-pressed={activePanel === "external"}
          >
            <Plus class="h-3.5 w-3.5" />
            Server
          </button>
        </div>
      </div>

      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {#each connManagerStore.profiles as profile (profile.id)}
          {@const state = stateFor(profile.id)}
          {@const isConnected = state.status === "connected"}
          {@const isConnecting = state.status === "connecting"}

          <!-- Profile card -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <div
            class="card bg-base-100 shadow transition-shadow
              {isConnected ? 'cursor-pointer ring-2 ring-success/40 hover:shadow-lg' : 'shadow-sm'}"
            onclick={() => handleCardClick(profile)}
            role={isConnected ? "button" : "article"}
            tabindex={isConnected ? 0 : -1}
            onkeydown={(e) => {
              if (isConnected && (e.key === "Enter" || e.key === " ")) {
                handleCardClick(profile);
              }
            }}
          >
            <div class="card-body gap-3 p-4">
              <!-- Name + type badge -->
              <div class="flex items-start justify-between gap-2">
                <div class="flex items-center gap-2 min-w-0">
                  {#if profile.type === "embedded"}
                    <Database class="text-primary h-5 w-5 shrink-0" strokeWidth={2} />
                  {:else}
                    <Server class="text-secondary h-5 w-5 shrink-0" strokeWidth={2} />
                  {/if}
                  <h3 class="font-semibold truncate">{profile.name}</h3>
                </div>
                <span
                  class="badge badge-sm shrink-0
                    {profile.type === 'embedded' ? 'badge-primary' : 'badge-secondary'}"
                >
                  {profile.type === "embedded" ? "Embedded" : "External"}
                </span>
              </div>

              <!-- Connection details -->
              <div class="text-base-content/60 text-xs">
                {#if profile.type === "external" && profile.external}
                  <span class="font-mono">{profile.external.address}</span>
                  <span class="ml-1 opacity-60">({profile.external.transport})</span>
                {:else if profile.type === "embedded" && profile.embedded}
                  <span class="capitalize">
                    {profile.serverMode === "raft" ? "Raft Cluster" : "Standalone"}
                  </span>
                  {#if profile.embedded.dataDir}
                    <span class="ml-1 font-mono opacity-60">{profile.embedded.dataDir}</span>
                  {/if}
                {/if}
              </div>

              <!-- Status badge -->
              <div class="flex items-center gap-2">
                <span class="badge {badgeClass(state.status)} badge-sm gap-1">
                  {#if isConnecting}
                    <span class="loading loading-spinner loading-xs"></span>
                  {:else if state.status === "connected"}
                    <CheckCircle2 class="h-3 w-3" />
                  {:else if state.status === "error"}
                    <AlertCircle class="h-3 w-3" />
                  {:else if state.status === "sealed"}
                    <Lock class="h-3 w-3" />
                  {/if}
                  {statusLabel(state.status)}
                </span>

                {#if state.status === "error" && state.error}
                  <span class="text-error truncate text-xs" title={state.error}>
                    {state.error}
                  </span>
                {/if}
              </div>

              <!-- Last used -->
              {#if profile.lastUsed}
                <div class="text-base-content/40 flex items-center gap-1 text-xs">
                  <Clock class="h-3 w-3" />
                  Last used {formatDate(profile.lastUsed)}
                </div>
              {/if}

              <!-- Actions -->
              <div class="card-actions mt-1 justify-end gap-2">
                {#if isConnected}
                  <button
                    class="btn btn-sm btn-ghost text-error"
                    onclick={(e) => {
                      e.stopPropagation();
                      handleDisconnect(profile.id);
                    }}
                    title="Disconnect"
                  >
                    <Unplug class="h-4 w-4" />
                    Disconnect
                  </button>
                {:else}
                  <button
                    class="btn btn-sm btn-primary"
                    disabled={isConnecting}
                    onclick={(e) => {
                      e.stopPropagation();
                      handleConnect(profile.id);
                    }}
                    title="Connect"
                  >
                    {#if isConnecting}
                      <span class="loading loading-spinner loading-xs"></span>
                    {:else}
                      <PlugZap class="h-4 w-4" />
                    {/if}
                    Connect
                  </button>
                {/if}

                {#if !profile.builtin}
                  <button
                    class="btn btn-sm btn-ghost text-error"
                    onclick={(e) => {
                      e.stopPropagation();
                      confirmDelete(profile.id);
                    }}
                    title="Delete profile"
                  >
                    <Trash2 class="h-4 w-4" />
                  </button>
                {/if}
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <!-- =========================================================
         Embedded server creation wizard
    ========================================================= -->
    {#if activePanel === "embedded"}
      <div class="card bg-base-100 mx-auto mt-6 w-full max-w-2xl shadow-xl">
        <div class="card-body gap-4">
          <!-- Wizard header -->
          <div>
            <h2 class="card-title">Create Embedded Server</h2>
            <p class="text-base-content/60 mt-1 text-sm">
              Configure and start a local QRDB server inside this application.
            </p>
          </div>

          <!-- Step indicator -->
          <StepIndicator steps={stepLabels} currentIndex={embeddedStepIndex} />

          <!-- ---- Step: name ---- -->
          {#if embeddedStep === "name"}
            <div class="space-y-4">
              <div class="form-control">
                <label class="label" for="emb-name">
                  <span class="label-text font-semibold">Server Name</span>
                </label>
                <input
                  id="emb-name"
                  type="text"
                  class="input input-bordered w-full"
                  placeholder="Local Server"
                  bind:value={embeddedName}
                />
              </div>

              <div class="form-control">
                <label class="label" for="emb-datadir">
                  <span class="label-text font-semibold">Data Directory</span>
                  <span class="label-text-alt text-base-content/50">optional</span>
                </label>
                <input
                  id="emb-datadir"
                  type="text"
                  class="input input-bordered w-full font-mono text-sm"
                  placeholder="~/.config/qrdb/data"
                  bind:value={embeddedDataDir}
                />
                <label class="label" for="emb-datadir">
                  <span class="label-text-alt text-base-content/50">
                    Root directory for all server data. Leave blank for the default location.
                  </span>
                </label>
              </div>
            </div>

            <div class="card-actions justify-end mt-2">
              <button
                class="btn btn-ghost"
                onclick={() => (activePanel = "none")}
              >
                Cancel
              </button>
              <button
                class="btn btn-primary"
                onclick={wizardNext}
                disabled={embeddedName.trim() === ""}
              >
                Next
                <ChevronRight class="h-4 w-4" />
              </button>
            </div>

          <!-- ---- Step: mode ---- -->
          {:else if embeddedStep === "mode"}
            <div class="space-y-4">
              <p class="text-base-content/70 text-sm">
                Choose the server topology. Standalone runs a single node. Raft
                Cluster enables multi-node distributed consensus.
              </p>

              <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <!-- Standalone card -->
                <button
                  type="button"
                  class="card cursor-pointer border-2 p-4 text-left transition-colors
                    {embeddedServerMode === 'standalone'
                      ? 'border-primary bg-primary/5'
                      : 'border-base-300 hover:border-primary/50'}"
                  onclick={() => (embeddedServerMode = "standalone")}
                >
                  <div class="flex items-start gap-3">
                    <input
                      type="radio"
                      name="emb-server-mode"
                      class="radio radio-primary mt-1"
                      value="standalone"
                      checked={embeddedServerMode === "standalone"}
                      onchange={() => (embeddedServerMode = "standalone")}
                    />
                    <div>
                      <div class="font-semibold">Standalone</div>
                      <span class="badge badge-sm badge-outline mt-1">Simple</span>
                      <p class="text-base-content/60 mt-1 text-sm">
                        Single-node server. No distributed consensus. Best for
                        local development and single-user deployments.
                      </p>
                    </div>
                  </div>
                </button>

                <!-- Raft cluster card -->
                <button
                  type="button"
                  class="card cursor-pointer border-2 p-4 text-left transition-colors
                    {embeddedServerMode === 'raft'
                      ? 'border-primary bg-primary/5'
                      : 'border-base-300 hover:border-primary/50'}"
                  onclick={() => (embeddedServerMode = "raft")}
                >
                  <div class="flex items-start gap-3">
                    <input
                      type="radio"
                      name="emb-server-mode"
                      class="radio radio-primary mt-1"
                      value="raft"
                      checked={embeddedServerMode === "raft"}
                      onchange={() => (embeddedServerMode = "raft")}
                    />
                    <div>
                      <div class="font-semibold">Raft Cluster</div>
                      <span class="badge badge-sm badge-success mt-1">Production</span>
                      <p class="text-base-content/60 mt-1 text-sm">
                        Multi-node Raft consensus with fault tolerance. Requires
                        peer nodes to join the cluster.
                      </p>
                    </div>
                  </div>
                </button>
              </div>
            </div>

            <div class="card-actions justify-between mt-2">
              <button class="btn btn-ghost" onclick={wizardBack}>Back</button>
              <button class="btn btn-primary" onclick={wizardNext}>
                Next
                <ChevronRight class="h-4 w-4" />
              </button>
            </div>

          <!-- ---- Step: discovery (raft only) ---- -->
          {:else if embeddedStep === "discovery"}
            <div class="space-y-4">
              <p class="text-base-content/70 text-sm">
                Choose how this server discovers other Raft cluster members.
              </p>

              <!-- Discovery method cards -->
              <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
                {#each (["multicast", "dns", "static"] as const) as method (method)}
                  <button
                    type="button"
                    class="card cursor-pointer border-2 p-4 text-left transition-colors
                      {embeddedDiscovery === method
                        ? 'border-primary bg-primary/5'
                        : 'border-base-300 hover:border-primary/50'}"
                    onclick={() => (embeddedDiscovery = method)}
                  >
                    <div class="flex items-start gap-3">
                      <input
                        type="radio"
                        name="emb-discovery"
                        class="radio radio-primary mt-1"
                        value={method}
                        checked={embeddedDiscovery === method}
                        onchange={() => (embeddedDiscovery = method)}
                      />
                      <div>
                        <div class="font-semibold capitalize">
                          {method === "dns" ? "DNS SRV" : method === "multicast" ? "Multicast" : "Static"}
                        </div>
                        <p class="text-base-content/60 mt-1 text-xs">
                          {#if method === "multicast"}
                            Automatic LAN discovery via multicast. Requires a shared secret.
                          {:else if method === "dns"}
                            Discover peers via DNS SRV records. Suitable for cloud deployments.
                          {:else}
                            Manually specify peer addresses. Most flexible option.
                          {/if}
                        </p>
                      </div>
                    </div>
                  </button>
                {/each}
              </div>

              <!-- Multicast secret -->
              {#if embeddedDiscovery === "multicast"}
                <div class="form-control">
                  <label class="label" for="emb-multicast-secret">
                    <span class="label-text font-semibold">Multicast Secret</span>
                    <span class="label-text-alt text-base-content/50">optional</span>
                  </label>
                  <input
                    id="emb-multicast-secret"
                    type="password"
                    class="input input-bordered w-full font-mono text-sm"
                    placeholder="shared-secret"
                    bind:value={embeddedMulticastSecret}
                  />
                  <label class="label" for="emb-multicast-secret">
                    <span class="label-text-alt text-base-content/50">
                      Only nodes with the same secret join this cluster.
                    </span>
                  </label>
                </div>
              {/if}

              <!-- DNS SRV domain -->
              {#if embeddedDiscovery === "dns"}
                <div class="form-control">
                  <label class="label" for="emb-dns-domain">
                    <span class="label-text font-semibold">DNS Domain</span>
                  </label>
                  <input
                    id="emb-dns-domain"
                    type="text"
                    class="input input-bordered w-full font-mono text-sm"
                    placeholder="qrdb.internal"
                    bind:value={embeddedDnsDomain}
                  />
                  <label class="label" for="emb-dns-domain">
                    <span class="label-text-alt text-base-content/50">
                      SRV records must exist at _qrdb._tcp.{embeddedDnsDomain || "domain"}.
                    </span>
                  </label>
                </div>
              {/if}

              <!-- Static peers -->
              {#if embeddedDiscovery === "static"}
                <div class="form-control">
                  <label class="label" for="emb-static-peers">
                    <span class="label-text font-semibold">Peer Addresses</span>
                  </label>
                  <textarea
                    id="emb-static-peers"
                    class="textarea textarea-bordered w-full font-mono text-sm"
                    rows="4"
                    placeholder={"node2.local:63001\nnode3.local:63001"}
                    bind:value={embeddedStaticPeers}
                  ></textarea>
                  <label class="label" for="emb-static-peers">
                    <span class="label-text-alt text-base-content/50">
                      One address per line. Host:port format.
                    </span>
                  </label>
                </div>
              {/if}
            </div>

            <div class="card-actions justify-between mt-2">
              <button class="btn btn-ghost" onclick={wizardBack}>Back</button>
              <button class="btn btn-primary" onclick={wizardNext}>
                Next
                <ChevronRight class="h-4 w-4" />
              </button>
            </div>

          <!-- ---- Step: barrier ---- -->
          {:else if embeddedStep === "barrier"}
            <div class="space-y-4">
              <p class="text-base-content/70 text-sm">
                Configure the server-level encryption barrier. All databases on
                this server are protected by this barrier at rest.
              </p>

              <!-- Barrier mode cards -->
              <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
                {#each (["none", "passphrase", "shamir"] as const) as mode (mode)}
                  <button
                    type="button"
                    class="card cursor-pointer border-2 p-4 text-left transition-colors
                      {embeddedBarrierMode === mode
                        ? 'border-primary bg-primary/5'
                        : 'border-base-300 hover:border-primary/50'}"
                    onclick={() => (embeddedBarrierMode = mode)}
                  >
                    <div class="flex items-start gap-3">
                      <input
                        type="radio"
                        name="emb-barrier-mode"
                        class="radio radio-primary mt-1"
                        value={mode}
                        checked={embeddedBarrierMode === mode}
                        onchange={() => (embeddedBarrierMode = mode)}
                      />
                      <div>
                        <div class="font-semibold capitalize">
                          {mode === "none" ? "None" : mode === "passphrase" ? "Passphrase" : "Shamir (M-of-N)"}
                        </div>
                        <p class="text-base-content/60 mt-1 text-xs">
                          {#if mode === "none"}
                            No encryption barrier. Data is stored unencrypted on disk.
                          {:else if mode === "passphrase"}
                            Barrier protected by a single passphrase.
                          {:else}
                            Split key across multiple holders. Requires M-of-N shares to unseal.
                          {/if}
                        </p>
                      </div>
                    </div>
                  </button>
                {/each}
              </div>

              <!-- None warning -->
              {#if embeddedBarrierMode === "none"}
                <div role="alert" class="alert alert-warning text-sm">
                  <AlertCircle class="h-5 w-5 shrink-0" />
                  <span>
                    Without an encryption barrier, all data is stored in plaintext on
                    disk. Only use this for non-sensitive development environments.
                  </span>
                </div>
              {/if}

              <!-- Passphrase inputs -->
              {#if embeddedBarrierMode === "passphrase"}
                <div class="space-y-3">
                  <div class="form-control">
                    <label class="label" for="emb-passphrase">
                      <span class="label-text font-semibold">Passphrase</span>
                    </label>
                    <input
                      id="emb-passphrase"
                      type="password"
                      class="input input-bordered w-full"
                      placeholder="Enter passphrase"
                      bind:value={embeddedPassphrase}
                    />
                  </div>
                  <div class="form-control">
                    <label class="label" for="emb-passphrase-confirm">
                      <span class="label-text font-semibold">Confirm Passphrase</span>
                    </label>
                    <input
                      id="emb-passphrase-confirm"
                      type="password"
                      class="input input-bordered w-full"
                      placeholder="Re-enter passphrase"
                      bind:value={embeddedPassphraseConfirm}
                    />
                  </div>
                </div>
              {/if}

              <!-- Shamir inputs -->
              {#if embeddedBarrierMode === "shamir"}
                <div class="bg-base-200 rounded-lg p-4 space-y-4">
                  <div role="alert" class="alert alert-info text-sm">
                    <AlertCircle class="h-5 w-5 shrink-0" />
                    <span>
                      The encryption key is split into <strong>N</strong> shares.
                      Any <strong>M</strong> shares can reconstruct it. Distribute
                      shares to different operators for security.
                    </span>
                  </div>
                  <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                      <label class="label" for="emb-shamir-threshold">
                        <span class="label-text font-semibold">Shares Required (M)</span>
                      </label>
                      <input
                        id="emb-shamir-threshold"
                        type="number"
                        class="input input-bordered w-full"
                        min="1"
                        max="10"
                        bind:value={embeddedShamirThreshold}
                      />
                    </div>
                    <div class="form-control">
                      <label class="label" for="emb-shamir-total">
                        <span class="label-text font-semibold">Total Shares (N)</span>
                      </label>
                      <input
                        id="emb-shamir-total"
                        type="number"
                        class="input input-bordered w-full"
                        min="1"
                        max="10"
                        bind:value={embeddedShamirShares}
                      />
                    </div>
                  </div>
                  {#if embeddedShamirThreshold > 0 && embeddedShamirShares >= embeddedShamirThreshold}
                    <p class="text-base-content/50 text-xs">
                      {embeddedShamirThreshold}-of-{embeddedShamirShares} scheme.
                      Tolerates loss of {embeddedShamirShares - embeddedShamirThreshold}
                      share{embeddedShamirShares - embeddedShamirThreshold !== 1 ? "s" : ""}.
                    </p>
                  {/if}
                </div>
              {/if}

              <!-- Validation error -->
              {#if barrierValidationError !== null}
                <div role="alert" class="alert alert-error py-2 text-sm">
                  <AlertCircle class="h-4 w-4 shrink-0" />
                  <span>{barrierValidationError}</span>
                </div>
              {/if}
            </div>

            <div class="card-actions justify-between mt-2">
              <button class="btn btn-ghost" onclick={wizardBack}>Back</button>
              <button
                class="btn btn-primary"
                onclick={wizardNext}
                disabled={!barrierValid}
              >
                Next
                <ChevronRight class="h-4 w-4" />
              </button>
            </div>

          <!-- ---- Step: summary ---- -->
          {:else if embeddedStep === "summary"}
            <div class="space-y-4">
              <p class="text-base-content/70 text-sm">
                Review your server configuration before creating it.
              </p>

              <div class="bg-base-200 rounded-lg divide-y divide-base-300">
                <!-- Name -->
                <div class="flex items-center justify-between px-4 py-3">
                  <span class="text-base-content/60 text-sm">Name</span>
                  <span class="font-semibold text-sm">{embeddedName}</span>
                </div>

                <!-- Data directory -->
                <div class="flex items-center justify-between px-4 py-3">
                  <span class="text-base-content/60 text-sm">Data Directory</span>
                  <span class="font-mono text-sm">
                    {embeddedDataDir || "~/.config/qrdb/data (default)"}
                  </span>
                </div>

                <!-- Server mode -->
                <div class="flex items-center justify-between px-4 py-3">
                  <span class="text-base-content/60 text-sm">Server Mode</span>
                  <span class="font-semibold text-sm capitalize">
                    {embeddedServerMode === "raft" ? "Raft Cluster" : "Standalone"}
                  </span>
                </div>

                <!-- Discovery (raft only) -->
                {#if embeddedServerMode === "raft"}
                  <div class="flex items-center justify-between px-4 py-3">
                    <span class="text-base-content/60 text-sm">Discovery</span>
                    <span class="font-semibold text-sm capitalize">
                      {embeddedDiscovery === "dns" ? "DNS SRV" : embeddedDiscovery}
                    </span>
                  </div>
                  {#if embeddedDiscovery === "dns" && embeddedDnsDomain}
                    <div class="flex items-center justify-between px-4 py-3">
                      <span class="text-base-content/60 text-sm">DNS Domain</span>
                      <span class="font-mono text-sm">{embeddedDnsDomain}</span>
                    </div>
                  {/if}
                  {#if embeddedDiscovery === "static" && parsedStaticPeers().length > 0}
                    <div class="flex items-start justify-between px-4 py-3">
                      <span class="text-base-content/60 text-sm">Static Peers</span>
                      <div class="flex flex-col items-end gap-1">
                        {#each parsedStaticPeers() as peer (peer)}
                          <span class="font-mono text-sm">{peer}</span>
                        {/each}
                      </div>
                    </div>
                  {/if}
                {/if}

                <!-- Barrier -->
                <div class="flex items-center justify-between px-4 py-3">
                  <span class="text-base-content/60 text-sm">Encryption Barrier</span>
                  <span class="font-semibold text-sm capitalize">
                    {embeddedBarrierMode === "none"
                      ? "None"
                      : embeddedBarrierMode === "passphrase"
                        ? "Passphrase"
                        : `Shamir (${embeddedShamirThreshold}-of-${embeddedShamirShares})`}
                  </span>
                </div>
              </div>

              {#if embeddedBarrierMode === "shamir"}
                <div role="alert" class="alert alert-warning text-sm">
                  <AlertCircle class="h-5 w-5 shrink-0" />
                  <span>
                    <strong>Important:</strong> After creation, you will be shown
                    {embeddedShamirShares} Shamir key shares. You must save all of
                    them before proceeding. They cannot be recovered later.
                  </span>
                </div>
              {/if}
            </div>

            <div class="card-actions justify-between mt-2">
              <button class="btn btn-ghost" onclick={wizardBack}>Back</button>
              <button class="btn btn-primary" onclick={startApplying}>
                Create Server
              </button>
            </div>

          <!-- ---- Step: applying ---- -->
          {:else if embeddedStep === "applying"}
            <div class="space-y-6">
              {#if shamirShareCount > 0 && applyProgress >= 100 && !applyError}
                <!-- Shamir barrier initialized: officer enrollment panel (share-free). -->
                <div class="space-y-4">
                  <div role="alert" class="alert alert-info text-sm">
                    <AlertCircle class="h-5 w-5 shrink-0" />
                    <span>
                      The barrier has been initialized with a
                      <strong>{shamirThresholdResult}-of-{shamirShareCount}</strong>
                      Shamir scheme. Each Security Officer must log in with their
                      <strong>mTLS certificate</strong> and navigate to
                      <strong>/officer/claim</strong> to retrieve their share.
                      Share bytes are never displayed here.
                    </span>
                  </div>

                  {#if shamirRootToken}
                    <div class="form-control">
                      <div class="label">
                        <span class="label-text font-semibold">Root Token</span>
                      </div>
                      <div class="bg-base-200 flex items-center gap-2 rounded px-3 py-2">
                        <span class="flex-1 font-mono text-sm break-all">{shamirRootToken}</span>
                        <button
                          type="button"
                          class="btn btn-ghost btn-xs btn-square shrink-0"
                          onclick={() => void copyToClipboard(shamirRootToken)}
                          title="Copy root token"
                        >
                          {#if copiedRootToken}
                            <Check class="h-3.5 w-3.5 text-success" />
                          {:else}
                            <Copy class="h-3.5 w-3.5" />
                          {/if}
                        </button>
                      </div>
                      <p class="text-xs text-base-content/50 mt-1">
                        Store this token securely. It can be regenerated from a share quorum if lost.
                      </p>
                    </div>
                  {/if}

                  <div class="card-actions justify-end">
                    <button
                      class="btn btn-primary"
                      onclick={handleShamirAck}
                    >
                      Continue to Server
                    </button>
                  </div>
                </div>
              {:else}
                <!-- Progress display -->
                <div class="w-full">
                  <progress
                    class="progress {applyError ? 'progress-error' : 'progress-primary'} w-full"
                    value={applyProgress}
                    max="100"
                  ></progress>
                  <div class="mt-2 flex justify-between text-sm">
                    <span class="text-base-content/60">{applyCurrentLabel}</span>
                    <span class="text-base-content/50">{applyProgress}%</span>
                  </div>
                </div>

                {#if !applyError}
                  <div class="flex justify-center">
                    <span class="loading loading-spinner loading-lg text-primary"></span>
                  </div>
                {/if}

                <!-- Phase indicators -->
                <div class="space-y-2">
                  {#each applyPhases as phase (phase.label)}
                    <div class="flex items-center gap-3">
                      {#if phase.status === "done"}
                        <CheckCircle2 class="h-5 w-5 text-success" />
                      {:else if phase.status === "active"}
                        <span class="loading loading-spinner loading-sm text-primary"></span>
                      {:else if phase.status === "error"}
                        <AlertCircle class="h-5 w-5 text-error" />
                      {:else}
                        <div class="h-5 w-5 rounded-full border-2 border-base-content/20"></div>
                      {/if}
                      <span
                        class="text-sm {phase.status === 'done' || phase.status === 'active'
                          ? 'text-base-content'
                          : phase.status === 'error'
                            ? 'text-error'
                            : 'text-base-content/40'}"
                      >
                        {phase.label}
                      </span>
                    </div>
                  {/each}
                </div>

                {#if applyError}
                  <div role="alert" class="alert alert-error text-sm">
                    <AlertCircle class="h-5 w-5 shrink-0" />
                    <span>{applyError}</span>
                  </div>
                  <div class="flex justify-center gap-2">
                    <button
                      class="btn btn-ghost"
                      onclick={() => {
                        activePanel = "none";
                        resetEmbeddedWizard();
                      }}
                    >
                      Cancel
                    </button>
                    <button class="btn btn-primary" onclick={handleRetryApply}>
                      Retry
                    </button>
                  </div>
                {/if}
              {/if}
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- =========================================================
         External server connection form (unchanged)
    ========================================================= -->
    {#if activePanel === "external"}
      <div class="card bg-base-100 mx-auto mt-6 w-full max-w-lg shadow-xl">
        <div class="card-body gap-4">
          <div>
            <h2 class="card-title">Connect to Server</h2>
            <p class="text-base-content/60 mt-1 text-sm">
              Connect to a remote QRDB daemon.
            </p>
          </div>

          <div class="form-control">
            <label class="label" for="ext-name">
              <span class="label-text">Name</span>
            </label>
            <input
              id="ext-name"
              type="text"
              class="input input-bordered w-full"
              placeholder="Remote Server"
              bind:value={externalName}
              disabled={externalSubmitting}
            />
          </div>

          <div class="grid grid-cols-2 gap-4">
            <div class="form-control">
              <label class="label" for="ext-transport">
                <span class="label-text">Transport</span>
              </label>
              <select
                id="ext-transport"
                class="select select-bordered w-full"
                bind:value={externalTransport}
                disabled={externalSubmitting}
              >
                <option value="grpc">gRPC</option>
                <option value="rest">REST (HTTP/2)</option>
                <option value="quic">QUIC</option>
                <option value="unix">Unix Socket</option>
                <option value="mcp">MCP</option>
              </select>
            </div>

            <div class="form-control">
              <label class="label" for="ext-address">
                <span class="label-text">Address</span>
              </label>
              <input
                id="ext-address"
                type="text"
                class="input input-bordered w-full font-mono text-sm"
                placeholder={defaultAddresses[externalTransport]}
                bind:value={externalAddress}
                disabled={externalSubmitting}
              />
            </div>
          </div>

          <!-- TLS options -->
          <div class="collapse collapse-arrow border-base-300 border rounded-lg">
            <input type="checkbox" />
            <div class="collapse-title text-sm font-medium">
              TLS / mTLS Options
              <span class="text-base-content/40 ml-1 font-normal">(optional)</span>
            </div>
            <div class="collapse-content space-y-3">
              <div class="form-control">
                <label class="label" for="ext-ca">
                  <span class="label-text text-sm">CA Certificate File</span>
                </label>
                <input
                  id="ext-ca"
                  type="text"
                  class="input input-bordered input-sm w-full font-mono"
                  placeholder="/path/to/ca.pem"
                  bind:value={externalCaFile}
                  disabled={externalSubmitting}
                />
              </div>
              <div class="form-control">
                <label class="label" for="ext-cert">
                  <span class="label-text text-sm">Client Certificate File</span>
                </label>
                <input
                  id="ext-cert"
                  type="text"
                  class="input input-bordered input-sm w-full font-mono"
                  placeholder="/path/to/client.pem"
                  bind:value={externalCertFile}
                  disabled={externalSubmitting}
                />
              </div>
              <div class="form-control">
                <label class="label" for="ext-key">
                  <span class="label-text text-sm">Client Key File</span>
                </label>
                <input
                  id="ext-key"
                  type="text"
                  class="input input-bordered input-sm w-full font-mono"
                  placeholder="/path/to/client-key.pem"
                  bind:value={externalKeyFile}
                  disabled={externalSubmitting}
                />
              </div>
            </div>
          </div>

          {#if externalError}
            <div role="alert" class="alert alert-error py-2 text-sm">
              <AlertCircle class="h-4 w-4 shrink-0" />
              <span>{externalError}</span>
            </div>
          {/if}

          <div class="card-actions justify-end gap-2 mt-2">
            <button
              class="btn btn-ghost"
              onclick={() => (activePanel = "none")}
              disabled={externalSubmitting}
            >
              Cancel
            </button>
            <button
              class="btn btn-primary"
              onclick={handleAddExternal}
              disabled={externalSubmitting || externalAddress.trim() === ""}
            >
              {#if externalSubmitting}
                <span class="loading loading-spinner loading-sm"></span>
              {/if}
              Connect
            </button>
          </div>
        </div>
      </div>
    {/if}
  </main>
</div>

<!-- Delete confirmation modal -->
{#if pendingDeleteId !== null}
  {@const profile = connManagerStore.profiles.find((p) => p.id === pendingDeleteId)}
  <div class="modal modal-open">
    <div class="modal-box">
      <h3 class="text-lg font-bold">Delete Connection</h3>
      <p class="text-base-content/70 mt-2 text-sm">
        Are you sure you want to delete
        <strong>{profile?.name ?? "this connection"}</strong>? This action cannot be undone.
      </p>
      <div class="modal-action">
        <button class="btn btn-ghost" onclick={handleDeleteCancel}>Cancel</button>
        <button class="btn btn-error" onclick={handleDeleteConfirm}>Delete</button>
      </div>
    </div>
    <button
      type="button"
      class="modal-backdrop"
      aria-label="Close modal"
      onclick={handleDeleteCancel}
    ></button>
  </div>
{/if}
