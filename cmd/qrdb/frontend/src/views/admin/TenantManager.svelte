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
  import type {
    Client,
    TenantInfo,
    TenantIsolationType,
    ListTenantsResponse,
    CreateTenantRequest,
    TenantAdminCredential,
    TenantBarrierInitRequest,
    TenantBarrierInitResponse,
    TenantBarrierStatusResponse,
    EnableEncryptionRequest,
    SealMode,
  } from "@qrdb/sdk";
  import { TransportError } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import {
    ChevronLeft,
    ShieldCheck,
    AlertTriangle,
    Unlock,
    Lock,
    Info,
    CheckCircle,
    Users,
    RefreshCw,
  } from "lucide-svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import HolderRekeyPanel from "../../components/HolderRekeyPanel.svelte";
  import HolderEnrolmentForms from "../../components/wizard/HolderEnrolmentForms.svelte";
  import TenantDetail from "./TenantDetail.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let tenants: TenantInfo[] = $state([]);
  let loading: boolean = $state(true);

  /** Controls visibility of the create form panel. */
  let showCreateForm: boolean = $state(false);

  /** Controls visibility of the barrier init panel. */
  let showBarrierForm: boolean = $state(false);

  /** Controls visibility of the quota detail panel. */
  let showQuotaDetail: boolean = $state(false);

  /** The tenant ID whose quotas are being edited. */
  let quotaDetailTenantId: string = $state("");

  /** The tenant selected for barrier initialization. */
  let barrierTenantId: string = $state("");

  // Create form fields
  let newTenantId: string = $state("");
  let newTenantName: string = $state("");
  // Default to prefix isolation: it works without a configured volume pool.
  // Barrier-isolation tenants require storage.volume_pools to be configured on
  // the daemon and are an advanced, deployment-specific choice.
  let newTenantIsolationType = $state<TenantIsolationType>("prefix");
  // Opt a dedicated-isolation tenant into the system-managed auto-unseal
  // key-management mode (entitlement-gated). Defaults to false (the secure
  // self-managed mode). Only meaningful for barrier/volume/raft isolation.
  let newTenantBarrierAutoUnseal: boolean = $state(false);
  // Named volume pool, valid ONLY for IsolationVolume and IsolationRaft
  // tenants. Mirrors the CLI's `tenant create --pool` flag; empty (the
  // default) uses the tenant's default pool, the first entry of its
  // allowedVolumePools. The field is only shown/sent for volume/raft.
  let newTenantVolumePool: string = $state("");
  // Pool names from client.volumePools.list(), loaded the first time the
  // pool field is shown. Empty (no pools, or the list call failed) keeps
  // the field free text so a pool mapped on the nodes but not yet listed
  // can still be named.
  let volumePoolNames: string[] = $state([]);
  let volumePoolNamesRequested = false;

  /** Loads the pool catalog's names for the create form's pool picker. */
  async function loadVolumePoolNames(): Promise<void> {
    try {
      const resp = await client.volumePools.list();
      volumePoolNames = (resp.pools ?? []).map((p) => p.name).sort();
    } catch {
      volumePoolNames = [];
    }
  }
  let createLoading: boolean = $state(false);

  // Admin onboarding fields. A tenant is created with its first admin's
  // login credential, or not at all: AdminUsername is always required, and
  // exactly one of a CSR (server-signed) or a bring-your-own certificate
  // (chained to the trust anchors below) is provided.
  let newAdminUsername: string = $state("");
  let newAdminCredentialKind = $state<"csr" | "cert">("csr");
  let newAdminCsrPem: string = $state("");
  let newAdminCertPem: string = $state("");
  let newAdminChainPem: string = $state("");
  let newTrustAnchorsPem: string = $state("");

  /** The created tenant's first admin, returned once by onboarding. Never a key. */
  let createdAdmin: TenantAdminCredential | null = $state(null);
  /** The tenant ID the just-created admin belongs to, for the result panel. */
  let createdTenantId: string = $state("");

  /**
   * Isolation modes offered by the create selector. The volume and raft tiers
   * are dedicated-storage tiers; multi-node Raft replication ships (replicas placed
   * across cluster nodes, RF configurable).
   */
  const isolationTypes: readonly {
    id: TenantIsolationType;
    label: string;
    description: string;
  }[] = [
    { id: "prefix", label: "prefix — namespace prefix only", description: "Shared-store namespace key prefix only. No extra crypto layer." },
    { id: "barrier", label: "barrier — namespace prefix + encryption barrier", description: "Namespace prefix plus a per-tenant encryption barrier. Requires a daemon volume pool." },
    { id: "volume", label: "volume — dedicated data volume (+ barrier)", description: "Dedicated filesystem data root per tenant. Dedicated-storage tier; requires a daemon volume pool." },
    { id: "raft", label: "raft — dedicated Raft group per database", description: "Dedicated Raft group per database on a dedicated data root. Dedicated-storage tier; multi-node replication ships: replicas are placed across cluster nodes (RF configurable)." },
  ] as const;

  /** True when the chosen isolation type is a dedicated tier with a barrier. */
  let isDedicatedIsolation = $derived(
    newTenantIsolationType === "barrier" ||
      newTenantIsolationType === "volume" ||
      newTenantIsolationType === "raft",
  );

  /**
   * True only for the dedicated-storage isolation tiers (volume/raft) that
   * place data on a named volume pool. Barrier isolation shares the
   * daemon's default store; prefix isolation is shared-store only. The
   * server ignores volume_pool for either.
   */
  let isVolumePoolEligible = $derived(
    newTenantIsolationType === "volume" || newTenantIsolationType === "raft",
  );

  // Clear the pool field whenever the isolation type moves away from
  // volume/raft so a stale value can never be submitted for prefix/barrier
  // (mirrors the CLI's validation instead of relying solely on the server's
  // typed rejection).
  $effect(() => {
    if (!isVolumePoolEligible && newTenantVolumePool !== "") {
      newTenantVolumePool = "";
    }
  });

  $effect(() => {
    if (isVolumePoolEligible && !volumePoolNamesRequested) {
      volumePoolNamesRequested = true;
      void loadVolumePoolNames();
    }
  });

  // Barrier init form fields. Per the platform architecture the tenant admin
  // chooses the per-tenant barrier strategy (none / passphrase / shamir);
  // passphrase is the back-compat default. The Shamir strategy initializes the
  // tenant barrier and seals each share to an enrolled holder, who claims it
  // from the CLI or the desktop app — NO shares are returned here.
  let barrierStrategy = $state<SealMode>("passphrase");
  let barrierPassphrase: string = $state("");
  let barrierPassphraseConfirm: string = $state("");
  let barrierThreshold: number = $state(3);
  let barrierShares: number = $state(5);
  let barrierLoading: boolean = $state(false);

  // Shamir init result (share-free: only threshold + shareCount).
  // Populated after a Shamir tenant barrier init to show holder claim status.
  let shamirResult = $state<TenantBarrierInitResponse | null>(null);
  // Holder claim status for the tenant barrier after Shamir init.
  let tenantOfficerStatuses = $state<import("@qrdb/sdk").OfficerStatus[]>([]);
  let tenantOfficerStatusLoading: boolean = $state(false);

  // Per-tenant share-holder panel: the holders of a Shamir tenant barrier, and
  // rekey and revocation rounds on it. A quorum of the current holders
  // completes a round from the CLI or the desktop app.
  let showHoldersPanel: boolean = $state(false);
  let holdersTenantId: string = $state("");
  let holders = $state<import("@qrdb/sdk").OfficerStatus[]>([]);
  let holdersLoading: boolean = $state(false);
  // Holder enrolment on the tenant barrier (registerTenantOfficer).
  let showEnrol: boolean = $state(false);
  let enrolRegistered: number = $state(0);

  /** Registers one holder on the open tenant and reloads the holder list. */
  async function enrolTenantHolder(
    req: import("@qrdb/sdk").OfficerRegisterRequest,
  ): Promise<import("@qrdb/sdk").OfficerRegisterResponse> {
    const tenantId = holdersTenantId;
    const resp = await client.officers.registerTenantOfficer({ tenantId, ...req });
    void fetchHolders(tenantId);
    return resp;
  }

  /** Opens the share-holder panel for a tenant. */
  function handleHolders(tenantId: string): void {
    holdersTenantId = tenantId;
    holders = [];
    showEnrol = false;
    enrolRegistered = 0;
    showHoldersPanel = true;
    void fetchHolders(tenantId);
  }

  /** Closes the share-holder panel. */
  function handleCloseHolders(): void {
    showHoldersPanel = false;
    holdersTenantId = "";
    holders = [];
  }

  /** Lists the tenant barrier's share holders. */
  async function fetchHolders(tenantId: string): Promise<void> {
    try {
      holdersLoading = true;
      holders = await client.officers.listTenantOfficers(tenantId);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : "Failed to list the tenant's share holders");
    } finally {
      holdersLoading = false;
    }
  }

  // Per-tenant unseal state. A passphrase barrier is unsealed here; a Shamir
  // barrier opens through a quorum of its holders, who submit from the CLI
  // or the desktop app with their own keys.
  let showUnsealForm: boolean = $state(false);
  let unsealTenantId: string = $state("");
  let unsealStatus = $state<TenantBarrierStatusResponse | null>(null);
  let unsealPassphraseInput: string = $state("");
  let unsealLoading: boolean = $state(false);
  let unsealStatusLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Per-tenant enable-encryption migration (committed "none" barrier → keyed)
  //
  // For a tenant whose DEDICATED barrier was initialized with strategy "none",
  // at-rest encryption is enabled by an in-process migration that re-encrypts
  // all of that tenant's data under a freshly minted keyed strategy. The request
  // always carries tenant_id, so the server scopes the migration to that
  // tenant's own barrier (never the shared system barrier). The migration is
  // asynchronous: after "migration_started" the tenant barrier status is polled
  // until its strategy changes away from "none". Mirrors the system-scope
  // wizard in SealManager.svelte. See docs/seal/enabling-encryption.md.
  // ---------------------------------------------------------------------------

  /** Controls visibility of the per-tenant enable-encryption wizard. */
  let showEnableForm: boolean = $state(false);
  /** The tenant whose dedicated barrier is being migrated to a keyed strategy. */
  let enableTenantId: string = $state("");
  let enableStrategy = $state<"passphrase" | "shamir">("passphrase");
  let enablePassphrase: string = $state("");
  let enableThreshold: number = $state(3);
  let enableTotal: number = $state(5);
  let enableMechanism = $state<"swap" | "in-place">("swap");
  let enableAvailability = $state<"quiesced" | "online">("quiesced");
  let enableLoading: boolean = $state(false);
  let enableInProgress: boolean = $state(false);
  let enableSuccess: boolean = $state(false);
  /** Keyed strategy the tenant barrier reports once the migration completes. */
  let enableResultStrategy: string = $state("");
  let enablePollTimer: ReturnType<typeof setInterval> | null = null;
  let enablePollAttempts: number = $state(0);

  /** Maximum status polls (~40 × 1.5s ≈ 60s) before surfacing a timeout. */
  const ENABLE_POLL_MAX_ATTEMPTS = 40;

  /** Strategy options for the per-tenant barrier selector. */
  const barrierStrategies: readonly { id: SealMode; label: string; description: string }[] = [
    { id: "none", label: "None", description: "No per-tenant encryption barrier; tenant data relies on the system barrier only." },
    { id: "passphrase", label: "Passphrase", description: "Passphrase-derived per-tenant barrier root key. Required at unseal." },
    { id: "shamir", label: "Shamir (M-of-N)", description: "Split the tenant barrier key into M-of-N shares; any M reconstruct it." },
  ] as const;

  /** Whether the current barrier-init inputs are valid for the chosen strategy. */
  let barrierValid = $derived(
    barrierStrategy === "none"
      ? true
      : barrierStrategy === "passphrase"
        ? barrierPassphrase.length >= 8 && barrierPassphrase === barrierPassphraseConfirm
        : barrierThreshold > 0 && barrierShares > 0 && barrierThreshold <= barrierShares,
  );

  // ---------------------------------------------------------------------------
  // Columns
  // ---------------------------------------------------------------------------

  const columns: Column<TenantInfo>[] = [
    {
      id: "id",
      header: "ID",
      accessor: "id",
      sortable: true,
      width: "25%",
    },
    {
      id: "name",
      header: "Name",
      accessor: "name",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "isolationType",
      header: "Isolation",
      accessor: (row: TenantInfo) => row.isolationType ?? "prefix",
      sortable: true,
      render: (value: unknown) => {
        switch (value) {
          case "barrier":
            return `<span class="badge badge-secondary badge-sm">barrier</span>`;
          case "volume":
            return `<span class="badge badge-accent badge-sm">volume</span>`;
          case "raft":
            return `<span class="badge badge-primary badge-sm">raft</span>`;
          default:
            return `<span class="badge badge-info badge-sm">prefix</span>`;
        }
      },
    },
    {
      id: "createdAt",
      header: "Created At",
      accessor: "createdAt",
      sortable: true,
    },
    {
      id: "updatedAt",
      header: "Updated At",
      accessor: "updatedAt",
      sortable: true,
    },
    {
      id: "quotas",
      header: "Quotas",
      accessor: (row: TenantInfo) => (row.quotas != null ? "Custom" : "Default"),
      sortable: false,
      render: (value: unknown) =>
        value === "Custom"
          ? `<span class="badge badge-accent badge-sm">Custom</span>`
          : `<span class="badge badge-ghost badge-sm">Default</span>`,
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the tenant list from the server. */
  async function fetchTenants(): Promise<void> {
    try {
      loading = true;
      const response: ListTenantsResponse = await client.tenants.list();
      tenants = [...(response.tenants ?? [])];
    } catch (err: unknown) {
      if (err instanceof TransportError && (err.statusCode === 404 || err.statusCode === 501 || err.statusCode === 503)) {
        tenants = [];
      } else {
        const msg = err instanceof Error ? err.message : "Failed to fetch tenants";
        showToast("error", msg);
      }
    } finally {
      loading = false;
    }
  }

  /** Opens the create form panel. */
  function handleCreate(): void {
    newTenantId = "";
    newTenantName = "";
    // Default to prefix isolation: it works without a configured volume pool.
    // Barrier isolation requires storage.volume_pools on the daemon.
    newTenantIsolationType = "prefix";
    newTenantBarrierAutoUnseal = false;
    newTenantVolumePool = "";
    // Re-read the pool catalog the next time the pool field is shown.
    volumePoolNamesRequested = false;
    newAdminUsername = "";
    newAdminCredentialKind = "csr";
    newAdminCsrPem = "";
    newAdminCertPem = "";
    newAdminChainPem = "";
    newTrustAnchorsPem = "";
    createdAdmin = null;
    createdTenantId = "";
    showCreateForm = true;
  }

  /** Cancels the create form and returns to the list. */
  function handleCancelCreate(): void {
    showCreateForm = false;
    createdAdmin = null;
    createdTenantId = "";
  }

  /** Submits the create tenant form. */
  async function handleSubmitCreate(): Promise<void> {
    if (!newTenantId.trim() || !newTenantName.trim()) {
      showToast("error", "Tenant ID and Name are required.");
      return;
    }
    if (!newAdminUsername.trim()) {
      showToast("error", "Admin username is required.");
      return;
    }
    if (newAdminCredentialKind === "csr" && !newAdminCsrPem.trim()) {
      showToast("error", "Admin CSR is required.");
      return;
    }
    if (newAdminCredentialKind === "cert" && !newAdminCertPem.trim()) {
      showToast("error", "Admin certificate is required.");
      return;
    }
    try {
      createLoading = true;
      const req: CreateTenantRequest = {
        id: newTenantId.trim(),
        name: newTenantName.trim(),
        isolationType: newTenantIsolationType,
        adminUsername: newAdminUsername.trim(),
        // Exactly one of adminCsrPem / adminCertPem, chosen by the form's
        // credential-kind toggle.
        ...(newAdminCredentialKind === "csr"
          ? { adminCsrPem: newAdminCsrPem.trim() }
          : {
              adminCertPem: newAdminCertPem.trim(),
              ...(newAdminChainPem.trim() === "" ? {} : { adminChainPem: newAdminChainPem.trim() }),
            }),
        ...(newTrustAnchorsPem.trim() === "" ? {} : { trustAnchorsPem: newTrustAnchorsPem.trim() }),
        // Only meaningful for a dedicated-isolation tenant; harmless otherwise.
        ...(isDedicatedIsolation && newTenantBarrierAutoUnseal
          ? { barrierAutoUnseal: true }
          : {}),
        // Only meaningful for IsolationVolume/IsolationRaft; ignored by the
        // server otherwise.
        ...(isVolumePoolEligible && newTenantVolumePool.trim() !== ""
          ? { volumePool: newTenantVolumePool.trim() }
          : {}),
      };
      const resp = await client.tenants.create(req);
      showToast("success", `Tenant "${newTenantName.trim()}" created.`);
      // Show the admin's certificate once, here — never a key. Keep the
      // form open on the result panel instead of returning to the list
      // immediately, so the admin cert can be copied or downloaded.
      createdAdmin = resp.admin;
      createdTenantId = resp.tenant.id;
      await fetchTenants();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to create tenant";
      showToast("error", msg);
    } finally {
      createLoading = false;
    }
  }

  /** Triggers a browser download of the given text content. */
  function downloadText(filename: string, content: string): void {
    const blob = new Blob([content], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  }

  /** Dismisses the admin-credential result panel and returns to the list. */
  function handleDismissCreatedAdmin(): void {
    showCreateForm = false;
    createdAdmin = null;
    createdTenantId = "";
  }

  /** Deletes selected tenants by their row indices. */
  async function handleDeleteSelected(indices: Set<number>): Promise<void> {
    const selected = [...indices]
      .map((i) => tenants[i])
      .filter((t): t is TenantInfo => t !== undefined);
    const failed: string[] = [];
    let deletedCount = 0;
    for (const tenant of selected) {
      try {
        await client.tenants.delete(tenant.id);
        deletedCount++;
      } catch {
        failed.push(tenant.id);
      }
    }
    if (failed.length === 0) {
      showToast("success", `Deleted ${deletedCount} tenant${deletedCount !== 1 ? "s" : ""}.`);
    } else {
      const msg = `Deleted ${deletedCount} tenant${deletedCount !== 1 ? "s" : ""} (${failed.length} failed: ${failed.join(", ")}).`;
      showToast("error", msg);
    }
    await fetchTenants();
  }

  /** Resets the barrier-init form fields to defaults. */
  function resetBarrierForm(): void {
    barrierStrategy = "passphrase";
    barrierPassphrase = "";
    barrierPassphraseConfirm = "";
    barrierThreshold = 3;
    barrierShares = 5;
    shamirResult = null;
    tenantOfficerStatuses = [];
  }

  /** Refreshes the holder claim status for a tenant barrier. */
  async function refreshTenantOfficerStatus(tenantId: string): Promise<void> {
    tenantOfficerStatusLoading = true;
    try {
      tenantOfficerStatuses = await client.officers.listTenantOfficers(tenantId);
    } catch {
      // Non-fatal.
    } finally {
      tenantOfficerStatusLoading = false;
    }
  }

  /** Opens the barrier init form for the specified tenant. */
  function handleBarrierInit(tenantId: string): void {
    barrierTenantId = tenantId;
    resetBarrierForm();
    showBarrierForm = true;
  }

  /** Cancels the barrier init form. */
  function handleCancelBarrier(): void {
    showBarrierForm = false;
    barrierTenantId = "";
    resetBarrierForm();
  }

  /** Dismisses the Shamir officer enrollment status panel. */
  function handleDismissShares(): void {
    showBarrierForm = false;
    barrierTenantId = "";
    resetBarrierForm();
    void fetchTenants();
  }

  // ---------------------------------------------------------------------------
  // Per-tenant unseal
  // ---------------------------------------------------------------------------

  /** Fetches the barrier status for the tenant being unsealed. */
  async function fetchUnsealStatus(tenantId: string): Promise<void> {
    try {
      unsealStatusLoading = true;
      const status = await client.tenants.barrierStatus(tenantId);
      unsealStatus = status;
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch tenant barrier status";
      showToast("error", msg);
    } finally {
      unsealStatusLoading = false;
    }
  }

  /** Opens the per-tenant unseal panel. */
  function handleUnseal(tenantId: string): void {
    unsealTenantId = tenantId;
    unsealPassphraseInput = "";
    unsealStatus = null;
    showUnsealForm = true;
    void fetchUnsealStatus(tenantId);
  }

  /** Closes the per-tenant unseal panel. */
  function handleCancelUnseal(): void {
    showUnsealForm = false;
    unsealTenantId = "";
    unsealPassphraseInput = "";
    unsealStatus = null;
  }

  /** Unseals a passphrase-held tenant barrier. */
  async function handleSubmitUnsealPassphrase(): Promise<void> {
    const key = unsealPassphraseInput;
    if (key === "") {
      showToast("warning", "The passphrase is required.");
      return;
    }
    try {
      unsealLoading = true;
      await client.tenants.barrierUnseal({ tenantId: unsealTenantId, key });
      unsealPassphraseInput = "";
      showToast("success", `Tenant "${unsealTenantId}" barrier unsealed.`);
      handleCancelUnseal();
      await fetchTenants();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to unseal the tenant barrier";
      showToast("error", msg);
      await fetchUnsealStatus(unsealTenantId);
    } finally {
      unsealLoading = false;
    }
  }

  /** Opens the quota detail panel for a tenant row that was clicked. */
  function handleSelectRow(event: RowClickEvent<TenantInfo>): void {
    quotaDetailTenantId = event.row.id;
    showQuotaDetail = true;
  }

  /** Closes the quota detail panel and refreshes the tenant list. */
  function handleQuotaDetailBack(): void {
    showQuotaDetail = false;
    quotaDetailTenantId = "";
    void fetchTenants();
  }

  /** Submits the barrier initialization form. */
  async function handleSubmitBarrier(): Promise<void> {
    if (!barrierValid) {
      if (barrierStrategy === "passphrase") {
        showToast(
          "error",
          barrierPassphrase.length < 8
            ? "Passphrase must be at least 8 characters."
            : "Passphrases do not match.",
        );
      } else {
        showToast("error", "Shares required must be greater than zero and not exceed total shares.");
      }
      return;
    }
    try {
      barrierLoading = true;
      // The tenant admin chooses the strategy. passphrase derives the tenant
      // barrier root key from the secret; shamir splits it M-of-N; none stores
      // tenant data under the system barrier only.
      const req: TenantBarrierInitRequest = {
        tenantId: barrierTenantId,
        strategy: barrierStrategy,
        threshold: barrierStrategy === "shamir" ? barrierThreshold : 0,
        shares: barrierStrategy === "shamir" ? barrierShares : 0,
        ...(barrierStrategy === "passphrase" ? { passphrase: barrierPassphrase } : {}),
      };
      const resp = await client.tenants.barrierInit(req);
      if (barrierStrategy === "shamir") {
        // Share bytes are never returned. Show the officer claim status panel
        // so the admin can monitor which SOs have claimed their shares.
        shamirResult = resp;
        showToast("success", `Shamir barrier initialized for tenant "${barrierTenantId}".`);
        // Load initial officer claim status.
        await refreshTenantOfficerStatus(barrierTenantId);
        return;
      }
      showToast("success", `Barrier initialized for tenant "${barrierTenantId}".`);
      showBarrierForm = false;
      barrierTenantId = "";
      resetBarrierForm();
      await fetchTenants();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to initialize barrier";
      showToast("error", msg);
    } finally {
      barrierLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Per-tenant enable-encryption migration
  // ---------------------------------------------------------------------------

  /** Resets the enable-encryption wizard fields to defaults. */
  function resetEnableForm(): void {
    enableStrategy = "passphrase";
    enablePassphrase = "";
    enableThreshold = 3;
    enableTotal = 5;
    enableMechanism = "swap";
    enableAvailability = "quiesced";
    enableInProgress = false;
    enableSuccess = false;
    enableResultStrategy = "";
    enablePollAttempts = 0;
  }

  /** Opens the enable-encryption wizard for the specified tenant. */
  function handleEnableEncryption(tenantId: string): void {
    stopEnablePolling();
    enableTenantId = tenantId;
    resetEnableForm();
    showEnableForm = true;
  }

  /** Cancels the enable-encryption wizard and stops any in-flight polling. */
  function handleCancelEnable(): void {
    stopEnablePolling();
    showEnableForm = false;
    enableTenantId = "";
    resetEnableForm();
  }

  /** Closes the wizard after a completed migration and refreshes the list. */
  function handleEnableDone(): void {
    handleCancelEnable();
    void fetchTenants();
  }

  /**
   * Starts the enable-encryption migration on the tenant's dedicated barrier
   * and begins polling for completion. The source barrier must currently be
   * "none"; the server rejects an already-keyed source (409), a tenant that
   * shares the system barrier (403), or invalid options (400) — those surface
   * as error toasts.
   */
  async function handleSubmitEnable(): Promise<void> {
    if (enableStrategy === "passphrase" && enablePassphrase.trim() === "") {
      showToast("warning", "A passphrase is required.");
      return;
    }
    try {
      enableLoading = true;
      // Build the request conditionally so optional fields are omitted (not set
      // to undefined) — required under exactOptionalPropertyTypes. tenant_id is
      // always set: this wizard drives the tenant's OWN dedicated barrier.
      const req: EnableEncryptionRequest = {
        tenantId: enableTenantId,
        strategy: enableStrategy,
        mechanism: enableMechanism,
        availability: enableAvailability,
        ...(enablePassphrase.trim() === "" ? {} : { passphrase: enablePassphrase }),
        ...(enableStrategy === "shamir"
          ? { shamirThreshold: enableThreshold, shamirTotal: enableTotal }
          : {}),
      };
      const resp = await client.seal.enableEncryption(req);
      if (resp.status === "migration_started") {
        enableInProgress = true;
        enablePollAttempts = 0;
        showToast("info", `Encryption migration started for tenant "${enableTenantId}".`);
        startEnablePolling();
      } else {
        showToast("info", resp.status);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to enable encryption";
      showToast("error", msg);
    } finally {
      enableLoading = false;
    }
  }

  /**
   * Polls the tenant barrier status until its strategy changes away from
   * "none", signalling that the migration has completed. The migration briefly
   * recycles the data plane, so transient status errors are ignored. Polling is
   * capped; on exhaustion a timeout is surfaced and the in-progress state ends.
   */
  function startEnablePolling(): void {
    stopEnablePolling();
    enablePollTimer = setInterval(() => {
      void (async () => {
        enablePollAttempts += 1;
        try {
          const status = await client.tenants.barrierStatus(enableTenantId);
          if (status.strategy !== "none" && status.strategy !== "") {
            stopEnablePolling();
            enableInProgress = false;
            enableSuccess = true;
            enableResultStrategy = status.strategy;
            showToast("success", `Encryption enabled for tenant "${enableTenantId}".`);
            return;
          }
        } catch {
          // Transient failure while the data plane recycles — keep polling.
        }
        if (enablePollAttempts >= ENABLE_POLL_MAX_ATTEMPTS) {
          stopEnablePolling();
          enableInProgress = false;
          showToast(
            "error",
            `Timed out waiting for the encryption migration of tenant "${enableTenantId}" to complete. It may still be running; refresh the tenant barrier status.`,
          );
        }
      })();
    }, 1500);
  }

  function stopEnablePolling(): void {
    if (enablePollTimer !== null) {
      clearInterval(enablePollTimer);
      enablePollTimer = null;
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchTenants();
    return () => {
      stopEnablePolling();
    };
  });
</script>

{#if showQuotaDetail}
  <TenantDetail {client} tenantId={quotaDetailTenantId} onBack={handleQuotaDetailBack} />

{:else if showCreateForm}
  <!-- Create tenant form -->
  <div class="flex flex-col gap-6">
    <div class="flex items-center gap-3">
      <button
        class="btn btn-ghost btn-sm"
        onclick={handleCancelCreate}
      >
        <ChevronLeft class="h-4 w-4" />
        Back
      </button>
      <h2 class="text-lg font-semibold">Create Tenant</h2>
    </div>

    <div class="card bg-base-200 w-full max-w-lg shadow">
      {#if createdAdmin != null}
        <!-- Admin credential result: shown/downloadable exactly once. No key
             is ever present here — the admin keeps its own. -->
        <div class="card-body gap-4" data-testid="create-tenant-admin-result">
          <div class="flex flex-col items-center gap-2 text-center">
            <CheckCircle class="text-success h-12 w-12" strokeWidth={1.5} />
            <h2 class="text-xl font-bold">Tenant Created</h2>
            <p class="text-base-content/70 text-sm">
              Tenant <span class="font-mono">{createdTenantId}</span> was created with admin
              <span class="font-mono">{createdAdmin.username}</span>.
              {#if createdAdmin.awaitsBarrierInit}
                This tenant keeps its identity records behind its own barrier; the admin's
                account is created once the admin initializes it with this certificate.
              {/if}
            </p>
          </div>

          <label class="form-control w-full">
            <div class="label"><span class="label-text text-xs font-semibold">Admin Certificate PEM</span></div>
            <textarea
              class="textarea textarea-bordered w-full font-mono text-xs"
              rows="8"
              readonly
              data-testid="create-tenant-admin-cert-result"
              value={createdAdmin.certPem}
            ></textarea>
            <button
              class="btn btn-ghost btn-xs mt-1 w-fit"
              onclick={() => downloadText(`${createdAdmin?.username ?? "admin"}.crt`, createdAdmin?.certPem ?? "")}
            >
              Download Certificate
            </button>
          </label>

          <div class="flex justify-end pt-2">
            <button
              class="btn btn-primary btn-sm"
              data-testid="create-tenant-admin-result-done"
              onclick={handleDismissCreatedAdmin}
            >
              Done
            </button>
          </div>
        </div>
      {:else}
      <div class="card-body gap-4">
        <div class="form-control">
          <label class="label" for="tenant-id">
            <span class="label-text font-semibold">Tenant ID</span>
          </label>
          <input
            id="tenant-id"
            type="text"
            class="input input-bordered w-full"
            placeholder="e.g. acme-corp"
            bind:value={newTenantId}
          />
        </div>

        <div class="form-control">
          <label class="label" for="tenant-name">
            <span class="label-text font-semibold">Name</span>
          </label>
          <input
            id="tenant-name"
            type="text"
            class="input input-bordered w-full"
            placeholder="e.g. Acme Corporation"
            bind:value={newTenantName}
          />
        </div>

        <div class="form-control">
          <label class="label" for="tenant-isolation">
            <span class="label-text font-semibold">Isolation Type</span>
            <span class="label-text-alt text-base-content/50">Physical keyspace strategy</span>
          </label>
          <select
            id="tenant-isolation"
            class="select select-bordered w-full"
            data-testid="create-tenant-isolation-select"
            bind:value={newTenantIsolationType}
          >
            {#each isolationTypes as it (it.id)}
              <option value={it.id}>{it.label}</option>
            {/each}
          </select>
          <div class="label">
            <span class="label-text-alt text-base-content/60">
              {isolationTypes.find((it) => it.id === newTenantIsolationType)?.description ?? ""}
            </span>
          </div>
          {#if newTenantIsolationType === "volume" || newTenantIsolationType === "raft"}
            <div class="label">
              <span class="label-text-alt text-info">
                Dedicated-storage tier. Multi-node Raft replication ships: replicas are placed across cluster nodes (RF configurable).
              </span>
            </div>
          {/if}
        </div>

        {#if isVolumePoolEligible}
          <div class="form-control">
            <label class="label" for="tenant-volume-pool">
              <span class="label-text font-semibold">Volume Pool</span>
              <span class="label-text-alt text-base-content/50">Optional</span>
            </label>
            {#if volumePoolNames.length > 0}
              <select
                id="tenant-volume-pool"
                class="select select-bordered w-full"
                data-testid="create-tenant-volume-pool"
                bind:value={newTenantVolumePool}
              >
                <option value="">Default pool</option>
                {#each volumePoolNames as poolName (poolName)}
                  <option value={poolName}>{poolName}</option>
                {/each}
              </select>
            {:else}
              <input
                id="tenant-volume-pool"
                type="text"
                class="input input-bordered w-full"
                placeholder="fast"
                data-testid="create-tenant-volume-pool"
                bind:value={newTenantVolumePool}
              />
            {/if}
            <div class="label">
              <span class="label-text-alt text-base-content/60">
                The named volume pool to place this tenant on. Leave empty to use the tenant's default pool.
              </span>
            </div>
          </div>
        {/if}

        {#if isDedicatedIsolation}
          <div class="form-control">
            <label class="label cursor-pointer justify-start gap-3" for="tenant-barrier-auto-unseal">
              <input
                id="tenant-barrier-auto-unseal"
                type="checkbox"
                class="toggle toggle-primary toggle-sm"
                data-testid="create-tenant-barrier-auto-unseal"
                bind:checked={newTenantBarrierAutoUnseal}
              />
              <span class="label-text font-semibold">System-managed barrier auto-unseal</span>
            </label>
            <div class="label">
              <span class="label-text-alt text-base-content/60">
                Opt this dedicated-isolation tenant into the system-managed auto-unseal key-management mode (entitlement-gated). Leave off for the secure self-managed mode where the tenant admin holds the key.
              </span>
            </div>
          </div>
        {/if}

        <div class="divider">First Admin</div>

        <div class="form-control">
          <label class="label" for="tenant-admin-username">
            <span class="label-text font-semibold">Admin Username</span>
            <span class="label-text-alt text-base-content/50">Required</span>
          </label>
          <input
            id="tenant-admin-username"
            type="text"
            class="input input-bordered w-full"
            placeholder="e.g. admin"
            data-testid="create-tenant-admin-username"
            bind:value={newAdminUsername}
          />
          <div class="label">
            <span class="label-text-alt text-base-content/60">
              A tenant is created with its first admin's login credential, or not at all.
            </span>
          </div>
        </div>

        <div class="form-control">
          <span class="label-text font-semibold mb-2">Admin Credential</span>
          <div class="join">
            <button
              type="button"
              class="join-item btn btn-sm {newAdminCredentialKind === 'csr' ? 'btn-active' : ''}"
              data-testid="create-tenant-admin-kind-csr"
              onclick={() => (newAdminCredentialKind = "csr")}
            >
              CSR (server-signed)
            </button>
            <button
              type="button"
              class="join-item btn btn-sm {newAdminCredentialKind === 'cert' ? 'btn-active' : ''}"
              data-testid="create-tenant-admin-kind-cert"
              onclick={() => (newAdminCredentialKind = "cert")}
            >
              Bring Your Own Certificate
            </button>
          </div>
        </div>

        {#if newAdminCredentialKind === "csr"}
          <label class="form-control w-full">
            <div class="label"><span class="label-text text-xs font-semibold">Admin CSR PEM</span></div>
            <textarea
              class="textarea textarea-bordered w-full font-mono text-xs"
              rows="4"
              placeholder="-----BEGIN CERTIFICATE REQUEST-----"
              data-testid="create-tenant-admin-csr"
              bind:value={newAdminCsrPem}
            ></textarea>
            <div class="label">
              <span class="label-text-alt text-base-content/60">
                Signed by the tenant's own CA; the admin keeps its key — it never leaves their machine.
              </span>
            </div>
          </label>
        {:else}
          <label class="form-control w-full">
            <div class="label"><span class="label-text text-xs font-semibold">Admin Certificate PEM</span></div>
            <textarea
              class="textarea textarea-bordered w-full font-mono text-xs"
              rows="4"
              placeholder="-----BEGIN CERTIFICATE-----"
              data-testid="create-tenant-admin-cert"
              bind:value={newAdminCertPem}
            ></textarea>
          </label>
          <label class="form-control w-full">
            <div class="label">
              <span class="label-text text-xs font-semibold">Admin Chain PEM</span>
              <span class="label-text-alt text-base-content/50">Optional</span>
            </div>
            <textarea
              class="textarea textarea-bordered w-full font-mono text-xs"
              rows="3"
              placeholder="Intermediate certificates"
              data-testid="create-tenant-admin-chain"
              bind:value={newAdminChainPem}
            ></textarea>
          </label>
        {/if}

        <label class="form-control w-full">
          <div class="label">
            <span class="label-text text-xs font-semibold">Trust Anchors PEM</span>
            <span class="label-text-alt text-base-content/50">Optional</span>
          </div>
          <textarea
            class="textarea textarea-bordered w-full font-mono text-xs"
            rows="3"
            placeholder="CA certificates to register as this tenant's trust anchors"
            data-testid="create-tenant-trust-anchors"
            bind:value={newTrustAnchorsPem}
          ></textarea>
          <div class="label">
            <span class="label-text-alt text-base-content/60">
              Registers one or more CA certificates as this tenant's trust anchors, so
              bring-your-own certificates issued by them can be enrolled later.
            </span>
          </div>
        </label>

        <div class="flex justify-end gap-3 pt-2">
          <button class="btn btn-ghost btn-sm" onclick={handleCancelCreate}>Cancel</button>
          <button
            class="btn btn-primary btn-sm"
            data-testid="create-tenant-submit"
            disabled={createLoading}
            onclick={handleSubmitCreate}
          >
            {#if createLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Create
          </button>
        </div>
      </div>
      {/if}
    </div>
  </div>

{:else if showBarrierForm}
  <!-- Barrier initialization form -->
  <div class="flex flex-col gap-6">
    <div class="flex items-center gap-3">
      <button
        class="btn btn-ghost btn-sm"
        onclick={handleCancelBarrier}
      >
        <ChevronLeft class="h-4 w-4" />
        Back
      </button>
      <h2 class="text-lg font-semibold">Initialize Barrier — {barrierTenantId}</h2>
    </div>

    <div class="card bg-base-200 w-full max-w-lg shadow">
      {#if shamirResult !== null}
        <!-- Shamir barrier initialized: show holder claim status. No share is
             displayed here — each holder claims its envelope with its own keys
             from the qrdb CLI or the desktop app. -->
        <div class="card-body gap-5">
          <div class="flex flex-col items-center gap-2 text-center">
            <ShieldCheck class="text-success h-12 w-12" strokeWidth={1.5} />
            <h2 class="text-xl font-bold">Tenant Shamir Barrier Initialized</h2>
          </div>

          <div role="alert" class="alert alert-info text-sm">
            <AlertTriangle class="h-5 w-5 shrink-0" />
            <span>
              A <strong>{shamirResult.threshold ?? 0}-of-{shamirResult.shareCount ?? shamirResult.totalShares ?? 0}</strong>
              Shamir scheme has been initialized. Each holder claims its sealed
              envelope over its own holder certificate from the qrdb CLI
              (<span class="font-mono">qrdb tenant barrier-officer claim --tenant-id {barrierTenantId}</span>)
              or the desktop app. No share is shown here.
            </span>
          </div>

          <!-- Holder claim status table -->
          <div class="form-control">
            <div class="flex items-center justify-between mb-2">
              <span class="label-text font-semibold">Holder Claim Status</span>
              <button
                class="btn btn-ghost btn-xs"
                onclick={() => void refreshTenantOfficerStatus(barrierTenantId)}
                disabled={tenantOfficerStatusLoading}
              >
                Refresh
              </button>
            </div>
            {#if tenantOfficerStatuses.length === 0}
              <p class="text-xs text-base-content/60">
                {tenantOfficerStatusLoading ? "Loading..." : "No officers registered yet, or claim status unavailable."}
              </p>
            {:else}
              <table class="table table-xs" data-testid="tenant-officer-claim-status-table">
                <thead>
                  <tr><th>Officer</th><th>Share #</th><th>Status</th></tr>
                </thead>
                <tbody>
                  {#each tenantOfficerStatuses as officer (officer.username)}
                    <tr>
                      <td class="font-mono">{officer.username}</td>
                      <td class="text-center">#{officer.shareIndex}</td>
                      <td>
                        {#if officer.claimedAt}
                          <span class="badge badge-success badge-sm">Claimed</span>
                        {:else}
                          <span class="badge badge-warning badge-sm">Pending</span>
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {/if}
          </div>

          <button
            class="btn btn-primary btn-block"
            data-testid="tenant-shamir-done-btn"
            onclick={handleDismissShares}
          >
            Done
          </button>
        </div>
      {:else}
      <div class="card-body gap-4">
        <p class="text-base-content/70 text-sm">
          Initialize this tenant's encryption barrier. The tenant admin chooses
          the protection strategy. A cryptographic barrier requires the daemon to
          have a storage volume pool configured.
        </p>

        <!-- Strategy selector -->
        <div class="form-control">
          <span class="label-text font-semibold mb-2">Barrier Strategy</span>
          <div class="grid grid-cols-3 gap-3">
            {#each barrierStrategies as strat (strat.id)}
              <button
                type="button"
                data-testid="barrier-strategy-{strat.id}"
                class="card cursor-pointer border-2 p-3 text-left transition-colors
                  {barrierStrategy === strat.id
                    ? 'border-primary bg-primary/5'
                    : 'border-base-300 hover:border-primary/50'}"
                onclick={() => (barrierStrategy = strat.id)}
              >
                <span class="font-semibold">{strat.label}</span>
                <p class="text-base-content/60 mt-1 text-xs">{strat.description}</p>
              </button>
            {/each}
          </div>
        </div>

        {#if barrierStrategy === "passphrase"}
          <div class="form-control">
            <label class="label" for="barrier-passphrase">
              <span class="label-text font-semibold">Passphrase</span>
              <span class="label-text-alt text-base-content/50">Minimum 8 characters</span>
            </label>
            <input
              id="barrier-passphrase"
              type="password"
              class="input input-bordered w-full"
              placeholder="Enter a strong passphrase"
              bind:value={barrierPassphrase}
            />
          </div>
          <div class="form-control">
            <label class="label" for="barrier-passphrase-confirm">
              <span class="label-text font-semibold">Confirm Passphrase</span>
            </label>
            <input
              id="barrier-passphrase-confirm"
              type="password"
              class="input input-bordered w-full"
              placeholder="Re-enter passphrase"
              bind:value={barrierPassphraseConfirm}
            />
          </div>
        {:else if barrierStrategy === "shamir"}
          <div class="alert alert-info text-sm">
            <span>
              The tenant barrier key is split into N shares; any M reconstruct
              it. Security Officers claim their individual shares via the
              officer claim flow — no shares are returned at initialization.
              Ensure officers are registered before initializing.
            </span>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div class="form-control">
              <label class="label" for="barrier-threshold">
                <span class="label-text font-semibold">Shares Required (M)</span>
              </label>
              <input
                id="barrier-threshold"
                type="number"
                class="input input-bordered w-full"
                min="1"
                max="10"
                bind:value={barrierThreshold}
              />
            </div>
            <div class="form-control">
              <label class="label" for="barrier-shares">
                <span class="label-text font-semibold">Total Shares (N)</span>
              </label>
              <input
                id="barrier-shares"
                type="number"
                class="input input-bordered w-full"
                min="1"
                max="10"
                bind:value={barrierShares}
              />
            </div>
          </div>
        {:else}
          <div class="alert alert-warning text-sm">
            <span>
              No per-tenant barrier: this tenant's data relies on the system
              barrier only.
            </span>
          </div>
        {/if}

        <div class="flex justify-end gap-3 pt-2">
          <button class="btn btn-ghost btn-sm" onclick={handleCancelBarrier}>Cancel</button>
          <button
            class="btn btn-warning btn-sm"
            title="Initialize the tenant encryption barrier with the chosen strategy."
            disabled={barrierLoading || !barrierValid}
            onclick={handleSubmitBarrier}
          >
            {#if barrierLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Initialize Barrier
          </button>
        </div>
      </div>
      {/if}
    </div>
  </div>

{:else if showHoldersPanel}
  <!-- Per-tenant share-holder panel -->
  <div class="flex flex-col gap-6" data-testid="tenant-holders-panel">
    <div class="flex items-center gap-3">
      <button class="btn btn-ghost btn-sm" onclick={handleCloseHolders}>
        <ChevronLeft class="h-4 w-4" />
        Back
      </button>
      <h2 class="text-lg font-semibold">Share Holders — {holdersTenantId}</h2>
    </div>

    <div class="card bg-base-200 w-full max-w-3xl shadow">
      <div class="card-body gap-4">
        <div class="flex items-center justify-between">
          <p class="text-base-content/70 text-sm">
            The holders of this tenant's Shamir barrier. Each holds one share of
            the unseal key and claims and submits from the qrdb CLI or the
            desktop app.
          </p>
          <button
            class="btn btn-ghost btn-sm gap-1"
            onclick={() => void fetchHolders(holdersTenantId)}
            disabled={holdersLoading}
            aria-label="Refresh share holders"
          >
            <RefreshCw class={`h-4 w-4 ${holdersLoading ? "animate-spin" : ""}`} />
            Refresh
          </button>
        </div>

        {#if holdersLoading && holders.length === 0}
          <div class="flex justify-center py-4">
            <span class="loading loading-spinner loading-sm"></span>
          </div>
        {:else if holders.length === 0}
          <div class="bg-base-300 rounded-lg p-4 text-center text-sm text-base-content/60">
            No share holders enrolled.
          </div>
        {:else}
          <div class="overflow-x-auto">
            <table class="table table-sm" data-testid="tenant-holders-table">
              <thead>
                <tr>
                  <th>Holder</th>
                  <th>Role</th>
                  <th>Share #</th>
                  <th>Claim</th>
                </tr>
              </thead>
              <tbody>
                {#each holders as holder (holder.username)}
                  <tr>
                    <td class="font-mono text-sm">{holder.username}</td>
                    <td class="text-xs">{holder.role}</td>
                    <td class="text-center">{holder.shareIndex}</td>
                    <td class="text-xs">
                      {holder.claimedAt ? "Claimed" : holder.sharePending ? "Pending" : "Not claimed"}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        <div class="border-t border-base-300 pt-4" data-testid="tenant-holder-enrol">
          <button
            class="btn btn-outline btn-sm"
            data-testid="tenant-holder-enrol-toggle"
            onclick={() => { showEnrol = !showEnrol; enrolRegistered = 0; }}
          >
            {showEnrol ? "Cancel enrolment" : "Enrol holder"}
          </button>
          {#if showEnrol}
            <div class="mt-4">
              <HolderEnrolmentForms
                {client}
                count={1}
                bind:registeredCount={enrolRegistered}
                enrol={enrolTenantHolder}
              />
            </div>
          {/if}
        </div>

        <div class="border-t border-base-300 pt-4">
          <HolderRekeyPanel
            submitCommand="qrdb tenant barrier-quorum submit --op rekey"
            onStartRekey={(r) => client.officers.startTenantRekey({
              tenantId: holdersTenantId,
              threshold: r.threshold,
              policy: r.policy,
            })}
            onRevoke={(r) => client.officers.revokeTenantOfficer({
              tenantId: holdersTenantId,
              username: r.username,
              ...(r.threshold > 0 ? { threshold: r.threshold } : {}),
            })}
          />
        </div>
      </div>
    </div>
  </div>

{:else if showUnsealForm}
  <!-- Per-tenant unseal panel -->
  <div class="flex flex-col gap-6">
    <div class="flex items-center gap-3">
      <button class="btn btn-ghost btn-sm" onclick={handleCancelUnseal}>
        <ChevronLeft class="h-4 w-4" />
        Back
      </button>
      <h2 class="text-lg font-semibold">Unseal Barrier — {unsealTenantId}</h2>
    </div>

    <div class="card bg-base-200 w-full max-w-lg shadow">
      <div class="card-body gap-4">
        {#if unsealStatusLoading && unsealStatus === null}
          <div class="flex justify-center py-6">
            <span class="loading loading-spinner loading-md"></span>
          </div>
        {:else if unsealStatus !== null && !unsealStatus.sealed}
          <p class="text-base-content/70 text-sm">This tenant's barrier is unsealed.</p>
        {:else if unsealStatus?.strategy === "shamir"}
          <!-- A Shamir barrier opens through a holder quorum; the holders'
               keys never enter this page. -->
          <p class="text-base-content/70 text-sm" data-testid="tenant-unseal-quorum">
            This barrier is held by its share holders:
            {unsealStatus.shamirThreshold ?? 0} of {unsealStatus.shamirTotal ?? 0} must
            submit. Each holder submits from the qrdb CLI
            (<span class="font-mono">qrdb tenant barrier-quorum submit --op unseal</span>)
            or the desktop app.
          </p>
          <button
            class="btn btn-outline btn-sm"
            onclick={() => void fetchUnsealStatus(unsealTenantId)}
            disabled={unsealStatusLoading}
          >
            Check again
          </button>
        {:else}
          <div class="form-control gap-3">
            <input
              type="password"
              class="input input-bordered w-full"
              placeholder="Unseal passphrase"
              data-testid="tenant-unseal-passphrase"
              bind:value={unsealPassphraseInput}
              onkeydown={(e: KeyboardEvent) => {
                if (e.key === "Enter") void handleSubmitUnsealPassphrase();
              }}
            />
            <button
              class="btn btn-primary btn-sm"
              onclick={() => void handleSubmitUnsealPassphrase()}
              disabled={unsealLoading || unsealPassphraseInput === ""}
            >
              {#if unsealLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Unseal
            </button>
          </div>
        {/if}
      </div>
    </div>
  </div>

{:else if showEnableForm}
  <!-- Per-tenant enable-encryption migration wizard -->
  <div class="flex flex-col gap-6">
    <div class="flex items-center gap-3">
      <button class="btn btn-ghost btn-sm" onclick={handleCancelEnable}>
        <ChevronLeft class="h-4 w-4" />
        Back
      </button>
      <h2 class="text-lg font-semibold">Enable Encryption — {enableTenantId}</h2>
    </div>

    <div class="card bg-base-200 w-full max-w-lg shadow">
      <div class="card-body gap-4" data-testid="tenant-enable-encryption-wizard">
        {#if enableSuccess}
          <div class="alert alert-success" data-testid="tenant-enable-encryption-success">
            <CheckCircle class="h-5 w-5 shrink-0" />
            <span class="text-sm">
              Encryption enabled for tenant <strong>{enableTenantId}</strong>. Its
              dedicated barrier now uses the
              <strong>{enableResultStrategy}</strong> strategy.
            </span>
          </div>
          <button class="btn btn-primary btn-sm" onclick={handleEnableDone}>
            Done
          </button>
        {:else if enableInProgress}
          <div class="alert alert-info" data-testid="tenant-enable-encryption-progress">
            <span class="loading loading-spinner loading-sm"></span>
            <span class="text-sm">
              Migration in progress — re-encrypting tenant
              <strong>{enableTenantId}</strong>'s at-rest data. This panel updates
              automatically when the migration completes.
            </span>
          </div>
        {:else}
          <div class="alert alert-info">
            <Info class="h-5 w-5 shrink-0" />
            <div class="text-sm">
              <p class="font-semibold">Enable encryption with an online migration.</p>
              <p class="mt-1">
                All of this tenant's stored data is re-encrypted under a new keyed
                strategy on its dedicated barrier. The data plane briefly pauses
                while the migration runs.
              </p>
            </div>
          </div>

          <div class="form-control">
            <label class="label" for="tenant-enable-strategy-select">
              <span class="label-text font-semibold">Target Strategy</span>
            </label>
            <select
              id="tenant-enable-strategy-select"
              class="select select-bordered select-sm"
              data-testid="tenant-enable-encryption-strategy"
              bind:value={enableStrategy}
            >
              <option value="passphrase">Passphrase</option>
              <option value="shamir">Shamir Secret Sharing</option>
            </select>
          </div>

          {#if enableStrategy === "passphrase"}
            <div class="form-control">
              <label class="label" for="tenant-enable-passphrase-input">
                <span class="label-text font-semibold">Passphrase</span>
              </label>
              <input
                id="tenant-enable-passphrase-input"
                type="password"
                class="input input-bordered input-sm"
                placeholder="Unseal passphrase"
                data-testid="tenant-enable-encryption-passphrase"
                bind:value={enablePassphrase}
              />
            </div>
          {:else}
            <div class="grid grid-cols-2 gap-4">
              <div class="form-control">
                <label class="label" for="tenant-enable-threshold">
                  <span class="label-text font-semibold">Threshold (M)</span>
                </label>
                <input
                  id="tenant-enable-threshold"
                  type="number"
                  class="input input-bordered input-sm"
                  min="1"
                  max={enableTotal}
                  bind:value={enableThreshold}
                />
              </div>
              <div class="form-control">
                <label class="label" for="tenant-enable-total">
                  <span class="label-text font-semibold">Total Shares (N)</span>
                </label>
                <input
                  id="tenant-enable-total"
                  type="number"
                  class="input input-bordered input-sm"
                  min={enableThreshold}
                  max="10"
                  bind:value={enableTotal}
                />
              </div>
            </div>
            <div class="form-control">
              <label class="label" for="tenant-enable-shamir-passphrase">
                <span class="label-text font-semibold">Passphrase (optional)</span>
              </label>
              <input
                id="tenant-enable-shamir-passphrase"
                type="password"
                class="input input-bordered input-sm"
                placeholder="Used to derive the master key"
                bind:value={enablePassphrase}
              />
            </div>
          {/if}

          <div class="grid grid-cols-2 gap-4">
            <div class="form-control">
              <label class="label" for="tenant-enable-mechanism">
                <span class="label-text font-semibold">Mechanism</span>
              </label>
              <select
                id="tenant-enable-mechanism"
                class="select select-bordered select-sm"
                bind:value={enableMechanism}
              >
                <option value="swap">Swap (default)</option>
                <option value="in-place">In-place</option>
              </select>
            </div>
            <div class="form-control">
              <label class="label" for="tenant-enable-availability">
                <span class="label-text font-semibold">Availability</span>
              </label>
              <select
                id="tenant-enable-availability"
                class="select select-bordered select-sm"
                bind:value={enableAvailability}
              >
                <option value="quiesced">Quiesced (default)</option>
                <option value="online">Online</option>
              </select>
            </div>
          </div>

          <div class="flex justify-end gap-3 pt-2">
            <button class="btn btn-ghost btn-sm" onclick={handleCancelEnable}>Cancel</button>
            <button
              class="btn btn-primary btn-sm"
              data-testid="tenant-enable-encryption-submit"
              title="Migrate this tenant's dedicated barrier to a keyed strategy, re-encrypting all of its at-rest data."
              disabled={enableLoading}
              onclick={() => void handleSubmitEnable()}
            >
              {#if enableLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              <Lock class="h-4 w-4" />
              Enable Encryption
            </button>
          </div>
        {/if}
      </div>
    </div>
  </div>

{:else}
  <!-- Tenant list -->
  <PageHeader title="Tenants" description="Manage tenant isolation, quotas, and encryption barriers" />
  <div class="flex flex-col gap-6">
    <CrudTable
      title="Tenants"
      data={tenants}
      {columns}
      {loading}
      pageSize={10}
      showCreate={true}
      showDelete={true}
      createLabel="Create Tenant"
      deleteLabel="Delete Selected"
      emptyMessage="No tenants found."
      oncreateclick={handleCreate}
      ondeleteselected={handleDeleteSelected}
      onrowclick={handleSelectRow}
    />

    <!-- Per-tenant barrier actions -->
    {#if tenants.length > 0}
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Barrier Management</h3>
          <p class="text-base-content/70 text-sm">
            Initialize or unseal the encryption barrier for a specific tenant.
          </p>
          <div class="flex flex-col gap-3 pt-2">
            {#each tenants as tenant (tenant.id)}
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-sm font-mono mr-1">{tenant.id}</span>
                <button
                  class="btn btn-outline btn-sm"
                  title="Initialize the encryption barrier for tenant {tenant.id}. For Shamir strategy, each enrolled holder claims its envelope from the CLI or the desktop app."
                  onclick={() => handleBarrierInit(tenant.id)}
                >
                  <ShieldCheck class="h-4 w-4" />
                  Init Barrier
                </button>
                <button
                  class="btn btn-outline btn-sm"
                  data-testid="tenant-holders-btn-{tenant.id}"
                  title="Share holders of tenant {tenant.id}'s Shamir barrier: list them, and open rekey and revocation rounds."
                  onclick={() => handleHolders(tenant.id)}
                >
                  <Users class="h-4 w-4" />
                  Holders
                </button>
                <button
                  class="btn btn-outline btn-sm"
                  title="Unseal the encryption barrier for tenant {tenant.id}: with its passphrase, or by a quorum of its share holders."
                  onclick={() => handleUnseal(tenant.id)}
                >
                  <Unlock class="h-4 w-4" />
                  Unseal
                </button>
                <button
                  class="btn btn-outline btn-sm"
                  data-testid="tenant-enable-encryption-btn-{tenant.id}"
                  title="Enable at-rest encryption for tenant {tenant.id} by migrating its dedicated barrier from a 'none' strategy to a keyed strategy."
                  onclick={() => handleEnableEncryption(tenant.id)}
                >
                  <Lock class="h-4 w-4" />
                  Enable Encryption
                </button>
              </div>
            {/each}
          </div>
        </div>
      </div>
    {/if}
  </div>
{/if}
