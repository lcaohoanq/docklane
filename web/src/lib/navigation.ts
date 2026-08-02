import type { ActiveTab } from "./types";

export function tabFromPath(pathname: string): ActiveTab {
  return pathname === "/containers" ? "containers" : "routes";
}

export function pathForTab(tab: ActiveTab) {
  return tab === "containers" ? "/containers" : "/routes";
}

export function navigateToTab(tab: ActiveTab, replace = false) {
  const path = pathForTab(tab);
  if (window.location.pathname === path) return;
  window.history[replace ? "replaceState" : "pushState"]({}, "", path);
}
