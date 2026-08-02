import type {
  Container,
  DiagnosticReport,
  HealthSnapshot,
  Route,
  RouteReadiness,
  WritableRoute,
} from "./types";

type ContainersPayload = { containers: Container[] };
type RoutesPayload = {
  routes: Route[];
  baseDomain: string;
  reconcileIntervalMs: number;
};

export class ApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(url: string, init?: RequestInit, fallback = "Request failed"): Promise<T> {
  const response = await fetch(url, init);
  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    payload = undefined;
  }
  if (!response.ok) {
    const message =
      typeof payload === "object" && payload && "error" in payload
        ? String(payload.error)
        : `${fallback} (${response.status})`;
    throw new ApiError(message, response.status);
  }
  return payload as T;
}

export async function loadInventory() {
  const [containers, routes] = await Promise.all([
    request<ContainersPayload>("/api/v1/containers", undefined, "Discovery failed"),
    request<RoutesPayload>("/api/v1/routes", undefined, "Route loading failed"),
  ]);
  return { ...routes, containers: containers.containers };
}

export function loadRouteReadiness(routeId: number) {
  return request<RouteReadiness>(`/api/v1/routes/${routeId}/readiness`, undefined, "Readiness check failed");
}

export function createRoute(route: WritableRoute) {
  return request<Route>("/api/v1/routes", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(route),
  }, "Save failed");
}

export function updateRoute(routeId: number, route: WritableRoute) {
  return request<Route>(`/api/v1/routes/${routeId}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(route),
  }, "Update failed");
}

export async function deleteRoute(routeId: number) {
  const response = await fetch(`/api/v1/routes/${routeId}`, { method: "DELETE" });
  if (!response.ok) {
    let message = `Delete failed (${response.status})`;
    try {
      const payload = await response.json();
      message = payload.error || message;
    } catch {
      // Keep the status-based fallback for non-JSON errors.
    }
    throw new ApiError(message, response.status);
  }
}

export function loadDiagnostics(routeId: number) {
  return request<DiagnosticReport>(`/api/v1/diagnostics/routes/${routeId}`, { cache: "no-store" }, "Diagnostics failed");
}

export function loadDiagnosticHistory(routeId: number) {
  return request<{ snapshots: HealthSnapshot[]; sampleIntervalMs: number }>(
    `/api/v1/diagnostics/routes/${routeId}/history?limit=50`,
    { cache: "no-store" },
    "History loading failed",
  );
}
