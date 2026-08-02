<script lang="ts">
  import ContainerTable from "./ContainerTable.svelte";
  import { filterContainers, groupContainers } from "../lib/container-utils";
  import type { Container } from "../lib/types";

  let { containers, loading, loadError, onRefresh, onChoose }: {
    containers: Container[];
    loading: boolean;
    loadError: string;
    onRefresh: () => void;
    onChoose: (container: Container) => void;
  } = $props();
  let search = $state("");
  let filtered = $derived(filterContainers(containers, search));
  let groups = $derived(groupContainers(filtered));
</script>

<section aria-labelledby="containers-title">
  <header class="page-header">
    <div><p class="eyebrow">DOCKER DISCOVERY</p><h1 id="containers-title">Containers</h1><p>Running workloads available to the local gateway.</p></div>
    <button class="btn btn-outline secondary" type="button" onclick={onRefresh} disabled={loading} aria-busy={loading}>
      {#if loading}<span class="loading loading-spinner loading-sm"></span>{:else}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6v5h-5M4 18v-5h5"></path><path d="M6.1 9A7 7 0 0 1 18.7 7.3L20 11M4 13l1.3 3.7A7 7 0 0 0 17.9 15"></path></svg>{/if}
      {loading ? "Refreshing…" : "Refresh Docker"}
    </button>
  </header>
  <div class="list-toolbar">
    <div class="title-with-count"><h2>Running containers</h2><span>{search ? `${filtered.length} of ${containers.length}` : `${containers.length} discovered`}</span></div>
    <div class="container-toolbar"><label class="input search-field">
      <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="6"></circle><path d="m16 16 4 4"></path></svg>
      <input type="search" bind:value={search} placeholder="Search containers" aria-label="Search containers" />
      {#if search}<button class="btn btn-circle btn-ghost btn-xs clear-search" type="button" aria-label="Clear container search" onclick={() => (search = "")}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m7 7 10 10M17 7 7 17"></path></svg></button>{/if}
    </label></div>
  </div>

  {#if loading && containers.length === 0}
    <div class="state-panel card bg-base-100" role="status"><span class="loading loading-spinner loading-md" aria-hidden="true"></span><div><h3>Finding containers</h3><p>Inspecting running Docker workloads…</p></div></div>
  {:else if loadError && containers.length === 0}
    <div class="state-panel state-error card bg-base-100" role="alert"><span class="state-icon">!</span><div><h3>Containers are unavailable</h3><p>{loadError}</p></div><button class="btn btn-outline btn-sm" type="button" onclick={onRefresh}>Try again</button></div>
  {:else if containers.length === 0}
    <div class="empty card bg-base-200"><span class="empty-icon"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 7h14M5 12h14M5 17h8"></path></svg></span><h3>No running containers</h3><p>Start a workload, then refresh Docker to create a route.</p><button class="btn btn-outline btn-sm" type="button" onclick={onRefresh}>Refresh Docker</button></div>
  {:else if filtered.length === 0}
    <div class="empty compact">No containers match “{search}”.</div>
  {:else}
    <div class="container-groups">
      {#each groups as group}
        {#if group.containers.length > 0}
          <section class="container-group" aria-labelledby={group.id}>
            <header class="container-group-heading"><div><h3 id={group.id}>{group.title}</h3><p>{group.description}</p></div><span>{group.containers.length}</span></header>
            <ContainerTable containers={group.containers} {onChoose} />
          </section>
        {/if}
      {/each}
    </div>
  {/if}
</section>
