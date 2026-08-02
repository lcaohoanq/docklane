<script lang="ts">
  import logoUrl from "../../../brand/logo-mark.svg";
  import type { ActiveTab, Theme } from "../lib/types";

  let {
    activeTab,
    routeCount,
    containerCount,
    theme,
    onNavigate,
    onThemeChange,
  }: {
    activeTab: ActiveTab;
    routeCount: number;
    containerCount: number;
    theme: Theme;
    onNavigate: (tab: ActiveTab) => void;
    onThemeChange: (theme: Theme) => void;
  } = $props();
</script>

<nav class="product-nav" aria-label="Primary navigation">
  <a class="brand" href="/routes" aria-label="Docklane routes" onclick={(event) => { event.preventDefault(); onNavigate("routes"); }}>
    <img src={logoUrl} alt="" />
    <span>Docklane</span>
  </a>
  <div class="product-tabs">
    <button type="button" class="btn btn-sm" class:active={activeTab === "routes"} aria-current={activeTab === "routes" ? "page" : undefined} onclick={() => onNavigate("routes")}>Routes <span>{routeCount}</span></button>
    <button type="button" class="btn btn-sm" class:active={activeTab === "containers"} aria-current={activeTab === "containers" ? "page" : undefined} onclick={() => onNavigate("containers")}>Containers <span>{containerCount}</span></button>
  </div>
  <div class="product-nav-meta">
    <a class="local-only controller-docs" href="https://lcaohoanq.github.io/docklane/docs/getting-started/quick-start/" target="_blank" rel="noreferrer" aria-label="Open Docklane quick start documentation">
      Local controller
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 5h5v5M19 5l-8 8"></path><path d="M18 13v5H6V6h5"></path></svg>
    </a>
    <button class="btn btn-circle btn-ghost theme-toggle" type="button" aria-label={`Switch to ${theme === "forest" ? "light" : "dark"} theme`} title={`Switch to ${theme === "forest" ? "light" : "dark"} theme`} onclick={() => onThemeChange(theme === "forest" ? "light" : "forest")}>
      {#if theme === "forest"}
        <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3.5"></circle><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"></path></svg>
      {:else}
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 15.2A8.3 8.3 0 0 1 8.8 4a8.3 8.3 0 1 0 11.2 11.2Z"></path></svg>
      {/if}
    </button>
  </div>
</nav>
