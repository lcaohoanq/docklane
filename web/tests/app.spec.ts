import { expect, test, type Page } from "@playwright/test";

const containers = [
  {
    id: "abc123",
    name: "budget-app-1",
    image: "example/budget:latest",
    status: "Up 8 minutes",
    composeProject: "budget",
    composeService: "app",
    exposedPorts: [3000],
    routeEligibility: { eligible: true },
  },
  {
    id: "gateway123",
    name: "docklane",
    image: "docklane:local",
    status: "Up 10 minutes",
    systemRole: "reverse-proxy",
    exposedPorts: [4646],
    routeEligibility: {
      eligible: false,
      code: "system-workload",
      reason: "System workload",
    },
  },
  {
    id: "probe123",
    name: "docklane-probe",
    image: "docklane:local",
    status: "Up 10 minutes (healthy)",
    systemRole: "probe",
    exposedPorts: [4646],
    routeEligibility: {
      eligible: false,
      code: "system-workload",
      reason: "System workload",
    },
  },
  {
    id: "buildkit123",
    name: "buildx_buildkit_release0",
    image: "moby/buildkit:buildx-stable-1",
    status: "Up 10 minutes",
    exposedPorts: [],
    routeEligibility: {
      eligible: false,
      code: "no-tcp-ports",
      reason: "No TCP ports declared",
    },
  },
];

const routes = [
  {
    id: 1,
    revision: 2,
    name: "budget",
    selector: { composeProject: "budget", composeService: "app" },
    port: 3000,
    scheme: "http",
    enabled: true,
    observed: {
      state: "ready",
      containerName: "budget-app-1",
      upstreamUrl: "http://budget-app-1:3000",
    },
  },
];

async function mockAPI(page: Page) {
  await page.route("https://budget.docker.home.arpa/**", async (request) =>
    request.fulfill({ status: 204 }),
  );
  await page.route("**/api/v1/**", async (request) => {
    const url = new URL(request.request().url());
    if (url.pathname === "/api/v1/containers") {
      return request.fulfill({ json: { containers } });
    }
    if (url.pathname === "/api/v1/routes") {
      return request.fulfill({
        json: {
          routes,
          baseDomain: "docker.home.arpa",
          reconcileIntervalMs: 5000,
        },
      });
    }
    if (url.pathname === "/api/v1/routes/1/readiness") {
      return request.fulfill({
        json: {
          routeId: 1,
          revision: 2,
          state: "ready",
          ready: true,
          message: "Route is active.",
          checkedAt: new Date().toISOString(),
        },
      });
    }
    if (url.pathname === "/api/v1/diagnostics/routes/1") {
      return request.fulfill({
        json: {
          status: "warn",
          target: "http://budget-app-1:3000",
          hostname: "budget.docker.home.arpa",
          generatedAt: new Date().toISOString(),
          checks: [
            {
              id: "container",
              layer: "Container",
              status: "pass",
              summary: "Container is running",
            },
            {
              id: "dns",
              layer: "Browser access",
              status: "warn",
              summary: "DNS response is slow",
              suggestion: "Check the local DNS resolver.",
            },
          ],
        },
      });
    }
    if (url.pathname === "/api/v1/diagnostics/routes/1/history") {
      return request.fulfill({
        json: { snapshots: [], retention: 288, sampleIntervalMs: 300000 },
      });
    }
    return request.fulfill({ status: 404, json: { error: "Not mocked" } });
  });
}

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
  await page.goto("/");
});

test("opens on the operational Routes view", async ({ page }) => {
  await expect(page).toHaveURL(/\/routes$/);
  await expect(page.getByRole("heading", { name: "Routes", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Home/ })).toHaveCount(0);
  await expect(page.getByLabel("Route status summary")).toContainText("Ready");
  await expect(page.getByText("budget.docker.home.arpa", { exact: true })).toBeVisible();
  await expect(page.locator(".mobile-label").first()).toBeHidden();
  await expect(
    page.getByRole("link", { name: "Open Docklane quick start documentation" }),
  ).toHaveAttribute(
    "href",
    "https://lcaohoanq.github.io/docklane/docs/getting-started/quick-start/",
  );

  const searchIcon = await page.locator(".search-field > svg").boundingBox();
  expect(searchIcon).not.toBeNull();
  expect(searchIcon!.width).toBeLessThanOrEqual(20);
  expect(searchIcon!.height).toBeLessThanOrEqual(20);
});

test("keeps the selected tab in the URL across refresh and browser history", async ({ page }) => {
  const indicator = page.locator(".product-tab-indicator");
  const routesPosition = await indicator.boundingBox();
  expect(routesPosition).not.toBeNull();

  await page.getByRole("button", { name: /Containers/ }).click();
  await expect(page).toHaveURL(/\/containers$/);
  await expect(indicator).toHaveAttribute("data-active-tab", "containers");
  await expect
    .poll(async () => (await indicator.boundingBox())?.x)
    .toBeGreaterThan(routesPosition!.x);
  await expect(page.getByRole("heading", { name: "Containers", exact: true })).toBeVisible();

  await page.reload();
  await expect(page).toHaveURL(/\/containers$/);
  await expect(page.getByRole("heading", { name: "Containers", exact: true })).toBeVisible();
  const containersPosition = await indicator.boundingBox();
  expect(containersPosition).not.toBeNull();

  await page.getByRole("button", { name: /Routes/ }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect
    .poll(async () => (await indicator.boundingBox())?.x)
    .toBeLessThan(containersPosition!.x);
  await page.goBack();
  await expect(page).toHaveURL(/\/containers$/);
  await expect
    .poll(async () => (await indicator.boundingBox())?.x)
    .toBeGreaterThan(routesPosition!.x);
  await expect(page.getByRole("heading", { name: "Containers", exact: true })).toBeVisible();
});

test("shows a useful loading state while discovery is pending", async ({ page }) => {
  await page.unroute("**/api/v1/**");
  let release!: () => void;
  const pending = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/api/v1/**", async (request) => {
    await pending;
    const path = new URL(request.request().url()).pathname;
    if (path === "/api/v1/containers") return request.fulfill({ json: { containers } });
    if (path === "/api/v1/routes") {
      return request.fulfill({
        json: { routes, baseDomain: "docker.home.arpa", reconcileIntervalMs: 5000 },
      });
    }
    return request.fulfill({ status: 404, json: { error: "Not mocked" } });
  });

  await page.reload();
  await expect(page.getByText("Loading routes")).toBeVisible();
  release();
  await expect(page.getByText("budget.docker.home.arpa", { exact: true })).toBeVisible();
});

test("offers retry when the controller is unavailable", async ({ page }) => {
  await page.unroute("**/api/v1/**");
  await page.route("**/api/v1/**", async (request) =>
    request.fulfill({ status: 503, json: { error: "Controller unavailable" } }),
  );

  await page.reload();
  await expect(page.getByRole("heading", { name: "Routes are unavailable" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(page.locator("[data-sonner-toast]")).toHaveCount(0);
});

test("deduplicates refresh failures after inventory has loaded", async ({ page }) => {
  await expect(page.getByText("budget.docker.home.arpa", { exact: true })).toBeVisible();
  await page.unroute("**/api/v1/**");
  await page.route("**/api/v1/**", async (request) =>
    request.fulfill({ status: 503, json: { error: "Controller unavailable" } }),
  );

  const refresh = page.getByRole("button", { name: "Refresh routes" });
  await refresh.click();
  await expect(page.locator("[data-sonner-toast]")).toHaveCount(1);
  await expect(page.locator("[data-sonner-toast]")).toContainText("Controller unavailable");
  const closeButton = await page.locator("[data-sonner-toast] [data-close-button]").boundingBox();
  expect(closeButton).not.toBeNull();
  expect(closeButton!.width).toBeLessThanOrEqual(24);
  expect(closeButton!.height).toBeLessThanOrEqual(24);

  await refresh.click();
  await expect(page.locator("[data-sonner-toast]")).toHaveCount(1);
});

test("switches between DaisyUI light and forest themes and persists the choice", async ({
  page,
}) => {
  await page.evaluate(() => localStorage.setItem("docklane-theme", "forest"));
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "forest");
  await page.getByRole("button", { name: "Switch to light theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("docklane-theme")))
    .toBe("light");

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  await page.getByRole("button", { name: "Switch to dark theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "forest");
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("docklane-theme")))
    .toBe("forest");
});

test("migrates the legacy dark preference to the forest theme", async ({ page }) => {
  await page.evaluate(() => localStorage.setItem("docklane-theme", "dark"));
  await page.reload();

  await expect(page.locator("html")).toHaveAttribute("data-theme", "forest");
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("docklane-theme")))
    .toBe("forest");
});

test("uses custom route actions and delete confirmation", async ({ page }) => {
  await page.getByRole("button", { name: "More actions for budget" }).click();
  await page.getByRole("button", { name: "Delete route" }).click();

  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("budget.docker.home.arpa");
  await expect(dialog).toContainText("The container is not changed");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
});

test("keeps route diagnostics compact until technical details are requested", async ({ page }) => {
  await page.getByRole("button", { name: "More actions for budget" }).click();
  await page.getByRole("button", { name: "Diagnose route" }).click();

  await expect(page.getByRole("heading", { name: "budget.docker.home.arpa" })).toBeVisible();
  await expect(page.getByText("Route opens in this browser")).toBeVisible();
  await expect(page.getByText("The route works, with warnings")).toBeVisible();
  await expect(page.getByText("1 item to check")).toBeVisible();

  const technicalDetails = page.getByText("Technical details", { exact: true });
  await expect(technicalDetails).toBeVisible();
  await expect(page.getByText("Container is running")).toBeHidden();
  await technicalDetails.click();
  await expect(page.getByText("Container is running")).toBeVisible();
});

test("shows route validation next to the invalid field", async ({ page }) => {
  await page.getByRole("button", { name: /Containers/ }).click();
  await page.getByRole("button", { name: "Create route" }).first().click();

  const drawer = page.getByRole("dialog", { name: /app/i });
  const hostname = drawer.getByLabel("Local hostname");
  await hostname.fill("Bad_Name");
  await drawer.getByRole("button", { name: "Create route" }).click();

  await expect(hostname).toHaveAttribute("aria-invalid", "true");
  await expect(
    drawer.getByText("Use lowercase letters, numbers, and single hyphens."),
  ).toBeVisible();
  await expect(hostname).toBeFocused();
});

test("only offers route creation for eligible containers", async ({ page }) => {
  await page.getByRole("button", { name: /Containers/ }).click();

  const groups = page.locator(".container-group");
  await expect(groups).toHaveCount(2);
  await expect(
    groups.first().getByRole("heading", { name: "Available for routing" }),
  ).toBeVisible();
  await expect(groups.last().getByRole("heading", { name: "Read-only containers" })).toBeVisible();
  await expect(groups.first().getByRole("button", { name: "Create route" })).toHaveCount(1);
  await expect(groups.last().getByRole("button", { name: "Create route" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Create route" })).toHaveCount(1);
  await expect(page.getByText("System workload", { exact: true })).toHaveCount(2);
  await expect(page.getByText("No TCP ports declared")).toBeVisible();
  await expect(page.locator(".workload-cell").filter({ hasText: "docklane-probe" })).toBeVisible();
  await expect(
    page.locator(".workload-cell").filter({ hasText: "buildx_buildkit_release0" }),
  ).toBeVisible();
});

test("keeps route actions usable at tablet width", async ({ page }) => {
  await page.setViewportSize({ width: 768, height: 900 });
  await expect(page.locator(".route-list-head")).toBeHidden();
  await page.getByRole("button", { name: "More actions for budget" }).click();
  await expect(page.getByRole("button", { name: "Diagnose route" })).toBeVisible();

  const horizontalOverflow = await page.evaluate(() =>
    Math.max(0, document.documentElement.scrollWidth - document.documentElement.clientWidth),
  );
  expect(horizontalOverflow).toBe(0);
});

test("creates routes from a mobile container card", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: /Containers/ }).click();
  await page.getByRole("button", { name: "Create route" }).first().click();

  const drawer = page.getByRole("dialog", { name: /app/i });
  await expect(drawer).toBeVisible();
  const drawerBox = await drawer.boundingBox();
  expect(drawerBox).not.toBeNull();
  expect(drawerBox!.x).toBeGreaterThanOrEqual(0);
  expect(drawerBox!.x + drawerBox!.width).toBeLessThanOrEqual(390);
  await expect(drawer.getByLabel("Local hostname")).toBeFocused();
  await drawer.getByLabel("Local hostname").fill("budget-new");
  await drawer.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog", { name: "Discard unsaved changes?" })).toBeVisible();

  const layout = await page.evaluate(() => ({
    overflow: Math.max(
      0,
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
    ),
    targets: [...document.querySelectorAll<HTMLButtonElement>("button")]
      .filter((button) => button.getBoundingClientRect().width > 0)
      .filter((button) => !button.classList.contains("modal-backdrop"))
      .filter((button) => !button.classList.contains("drawer-backdrop"))
      .map((button) => button.getBoundingClientRect().height),
  }));
  expect(layout.overflow).toBe(0);
  expect(layout.targets.length).toBeGreaterThan(0);
  expect(Math.min(...layout.targets)).toBeGreaterThanOrEqual(44);
});
