<script lang="ts">
  import { uniquePorts } from "../lib/container-utils";
  import type { Container } from "../lib/types";

  let { containers, onChoose }: { containers: Container[]; onChoose: (container: Container) => void } = $props();
</script>

<div class="container-table-scroll card bg-base-100">
  <div class="container-table">
    <div class="container-table-head" aria-hidden="true"><span>Workload</span><span class="image-column">Image</span><span>Ports</span><span class="state-column">State</span><span></span></div>
    {#each containers as container}
      {@const managed = container.routeEligibility.code === "system-workload"}
      <div class:managed class="container-table-row" title={!container.routeEligibility.eligible ? container.routeEligibility.reason : undefined}>
        <span class="workload-cell"><strong>{container.composeService || container.name}</strong><small>{container.name}</small></span>
        <span class="image-cell image-column">{container.image}</span>
        <span class="ports-cell">
          {#each uniquePorts(container.exposedPorts) as port}<code>:{port}</code>{:else}<small>None declared</small>{/each}
        </span>
        <span class="container-state state-column"><i class="status-dot" aria-hidden="true"></i>
          {#if managed}<span class="system-badge">{container.systemRole === "reverse-proxy" ? "gateway" : container.systemRole || "system"}</span>{:else}<span>{container.status}</span>{/if}
        </span>
        <span class="container-action">
          {#if container.routeEligibility.eligible}
            <button class="btn btn-outline btn-sm secondary small" type="button" onclick={() => onChoose(container)}>Create route <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"></path></svg></button>
          {:else}<span class="managed-note">{container.routeEligibility.reason}</span>{/if}
        </span>
      </div>
    {/each}
  </div>
</div>
