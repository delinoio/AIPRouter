import { defineConfig } from "@rspress/core";

export default defineConfig({
  root: "docs",
  outDir: "dist",
  title: "AIPRouter",
  description: "Public guides for self-hosting and using AIPRouter.",
  lang: "en",
  builderConfig: {
    server: { host: "127.0.0.1", port: 4174, strictPort: true },
  },
  themeConfig: {
    nav: [{ text: "Guides", link: "/guide/installation" }],
    sidebar: {
      "/": [
        { text: "Introduction", link: "/" },
        { text: "Install", link: "/guide/installation" },
        { text: "Configure services", link: "/guide/configuration" },
        { text: "Upgrade and recover", link: "/guide/recovery" },
        { text: "Organizations and invitations", link: "/guide/organizations" },
        { text: "API keys and clients", link: "/guide/clients" },
        {
          text: "Optional native compatibility",
          link: "/guide/native-compatibility",
        },
        { text: "Providers and routing", link: "/guide/providers" },
        { text: "Compatibility", link: "/guide/compatibility" },
        { text: "Observe and troubleshoot", link: "/guide/operations" },
        { text: "Manual migration", link: "/guide/migration" },
      ],
    },
  },
});
