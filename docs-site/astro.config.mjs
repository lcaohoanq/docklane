import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import mermaid from "astro-mermaid";

const base = "/docklane";

export default defineConfig({
  site: "https://lcaohoanq.github.io",
  base,
  integrations: [
    mermaid({
      autoTheme: true,
      mermaidConfig: {
        flowchart: {
          curve: "basis",
          htmlLabels: true,
        },
        themeVariables: {
          fontFamily: "Inter Variable, sans-serif",
          primaryColor: "#e8f7ef",
          primaryTextColor: "#14251d",
          primaryBorderColor: "#159455",
          lineColor: "#60736a",
          secondaryColor: "#f4f8f6",
          tertiaryColor: "#ffffff",
        },
      },
    }),
    starlight({
      title: "Docklane",
      description:
        "Stable local HTTPS names for Docker containers, managed through one safe and inspectable gateway.",
      favicon: "/favicon.svg",
      logo: {
        src: "./src/assets/logo-mark.svg",
        alt: "Docklane",
      },
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/lcaohoanq/docklane",
        },
      ],
      editLink: {
        baseUrl:
          "https://github.com/lcaohoanq/docklane/edit/main/docs-site/",
      },
      customCss: ["./src/styles/brand.css"],
      components: {
        Footer: "./src/components/Footer.astro",
        Head: "./src/components/Head.astro",
      },
      sidebar: [
        {
          label: "Getting started",
          items: [
            { label: "Overview", slug: "docs" },
            { label: "Requirements", slug: "docs/getting-started/requirements" },
            { label: "Quick start", slug: "docs/getting-started/quick-start" },
            { label: "Install", slug: "docs/getting-started/installation" },
            { label: "Uninstall", slug: "docs/getting-started/uninstall" },
          ],
        },
        {
          label: "Guides",
          items: [{ autogenerate: { directory: "docs/guides" } }],
        },
        {
          label: "Concepts",
          items: [{ autogenerate: { directory: "docs/concepts" } }],
        },
        {
          label: "Reference",
          items: [{ autogenerate: { directory: "docs/reference" } }],
        },
        {
          label: "Architecture",
          items: [{ autogenerate: { directory: "docs/architecture" } }],
          collapsed: true,
        },
        {
          label: "Troubleshooting",
          items: [
            { label: "Troubleshooting", slug: "docs/troubleshooting" },
          ],
        },
        {
          label: "Releases",
          items: [{ autogenerate: { directory: "docs/releases" } }],
          collapsed: true,
        },
      ],
      head: [
        {
          tag: "meta",
          attrs: {
            property: "og:image",
            content:
              "https://lcaohoanq.github.io/docklane/social-card.png",
          },
        },
        {
          tag: "meta",
          attrs: { name: "twitter:card", content: "summary_large_image" },
        },
      ],
    }),
  ],
});
