<script lang="ts">
  import type { Route, RouteReadiness } from "../lib/types";

  let { route, readiness, baseDomain, highlighted, menuOpen, pending, onMenu, onDiagnose, onEdit, onToggle, onDelete }: {
    route: Route;
    readiness: RouteReadiness;
    baseDomain: string;
    highlighted: boolean;
    menuOpen: boolean;
    pending: boolean;
    onMenu: () => void;
    onDiagnose: () => void;
    onEdit: () => void;
    onToggle: () => void;
    onDelete: () => void;
  } = $props();
</script>

<div class:highlighted class="route-row">
  <div class="route-status-cell"><span class={`route-state ${readiness.state}`} title={readiness.message}></span><span class="mobile-label">{readiness.state}</span></div>
  <div class="route-name">
    {#if readiness.ready}
      <a href={`https://${route.name}.${baseDomain}`} target="_blank" rel="noreferrer">{route.name}.{baseDomain}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 5h5v5M19 5l-8 8"></path><path d="M18 13v5H6V6h5"></path></svg></a>
    {:else}<span class="route-link-pending" title="The link unlocks after Traefik confirms the route.">{route.name}.{baseDomain}</span>{/if}
    <small>{readiness.state}{#if !readiness.ready} · {readiness.message}{/if}</small>
  </div>
  <div class="route-workload"><strong>{route.selector.composeService || route.observed?.containerName || "Container"}</strong><small>{route.selector.composeProject || route.observed?.containerName || "Direct selector"}</small></div>
  <code class="route-upstream">{route.scheme}://:{route.port}</code>
  <div class="route-actions">
    <button class="btn btn-square btn-ghost btn-sm icon-button ghost small" type="button" aria-label={`More actions for ${route.name}`} aria-expanded={menuOpen} onclick={onMenu} disabled={pending}>
      {#if pending}<span class="spinner"></span>{:else}<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="12" r="1"></circle><circle cx="12" cy="12" r="1"></circle><circle cx="19" cy="12" r="1"></circle></svg>{/if}
    </button>
    {#if menuOpen}
      <div class="menu action-menu">
        <button class="btn btn-ghost btn-sm" type="button" onclick={onDiagnose}>Diagnose route</button>
        <button class="btn btn-ghost btn-sm" type="button" onclick={onEdit}>Edit route</button>
        <button class="btn btn-ghost btn-sm" type="button" onclick={onToggle}>{route.enabled ? "Disable route" : "Enable route"}</button>
        <span></span><button class="btn btn-ghost btn-sm danger" type="button" onclick={onDelete}>Delete route</button>
      </div>
    {/if}
  </div>
</div>
