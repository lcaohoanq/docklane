<script lang="ts">
  import { untrack } from "svelte";
  import { cubicOut } from "svelte/easing";
  import { Tween } from "svelte/motion";
  import { MediaQuery } from "svelte/reactivity";
  import logoUrl from "../../../brand/logo-mark.svg";
  import type { ActiveTab, Theme } from "../lib/types";

  const prefersReducedMotion = new MediaQuery("(prefers-reduced-motion: reduce)");
  const indicatorLeft = new Tween(0, { duration: 220, easing: cubicOut });
  const indicatorWidth = new Tween(0, { duration: 220, easing: cubicOut });

  let routesTab: HTMLButtonElement;
  let containersTab: HTMLButtonElement;
  let indicatorReady = $state(false);

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

  function moveIndicator(animate = true) {
    const activeButton = activeTab === "routes" ? routesTab : containersTab;
    if (!activeButton) return;

    const duration = animate && !prefersReducedMotion.current ? 220 : 0;
    void indicatorLeft.set(activeButton.offsetLeft, { duration, easing: cubicOut });
    void indicatorWidth.set(activeButton.offsetWidth, { duration, easing: cubicOut });
    indicatorReady = true;
  }

  $effect(() => {
    const routeButton = routesTab;
    const containerButton = containersTab;
    if (!routeButton || !containerButton) return;

    untrack(() => moveIndicator(false));
    const observer = new ResizeObserver(() => untrack(() => moveIndicator(false)));
    observer.observe(routeButton);
    observer.observe(containerButton);
    return () => observer.disconnect();
  });

  $effect(() => {
    activeTab;
    if (indicatorReady) moveIndicator(true);
  });
</script>

<nav class="product-nav" aria-label="Primary navigation">
  <a
    class="brand"
    href="/routes"
    aria-label="Docklane routes"
    onclick={(event) => {
      event.preventDefault();
      onNavigate("routes");
    }}
  >
    <img src={logoUrl} alt="" />
    <span>Docklane</span>
  </a>
  <div class="product-tabs">
    <span
      class="product-tab-indicator"
      class:initialized={indicatorReady}
      data-active-tab={activeTab}
      aria-hidden="true"
      style:left={`${indicatorLeft.current}px`}
      style:width={`${indicatorWidth.current}px`}
    ></span>
    <button
      bind:this={routesTab}
      type="button"
      class="btn btn-sm"
      class:active={activeTab === "routes"}
      aria-current={activeTab === "routes" ? "page" : undefined}
      onclick={() => onNavigate("routes")}
      >Routes <span class="product-tab-count">{routeCount}</span></button
    >
    <button
      bind:this={containersTab}
      type="button"
      class="btn btn-sm"
      class:active={activeTab === "containers"}
      aria-current={activeTab === "containers" ? "page" : undefined}
      onclick={() => onNavigate("containers")}
      >Containers <span class="product-tab-count">{containerCount}</span></button
    >
  </div>
  <div class="product-nav-meta">
    <a
      class="local-only controller-docs"
      href="https://lcaohoanq.github.io/docklane/docs/getting-started/quick-start/"
      target="_blank"
      rel="noreferrer"
      aria-label="Open Docklane quick start documentation"
    >
      Local controller
      <svg viewBox="0 0 24 24" aria-hidden="true"
        ><path d="M14 5h5v5M19 5l-8 8"></path><path d="M18 13v5H6V6h5"></path></svg
      >
    </a>
    <button
      class="btn btn-circle btn-ghost theme-toggle"
      type="button"
      aria-label={`Switch to ${theme === "forest" ? "light" : "dark"} theme`}
      title={`Switch to ${theme === "forest" ? "light" : "dark"} theme`}
      onclick={() => onThemeChange(theme === "forest" ? "light" : "forest")}
    >
      {#if theme === "forest"}
        <svg viewBox="0 0 24 24" aria-hidden="true"
          ><circle cx="12" cy="12" r="3.5"></circle><path
            d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"
          ></path></svg
        >
      {:else}
        <svg viewBox="0 0 24 24" aria-hidden="true"
          ><path d="M20 15.2A8.3 8.3 0 0 1 8.8 4a8.3 8.3 0 1 0 11.2 11.2Z"></path></svg
        >
      {/if}
    </button>
  </div>
</nav>
