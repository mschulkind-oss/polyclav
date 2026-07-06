import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

// The app is served by the polyclav daemon from /app/ (see
// internal/web/static.go), so every page and asset URL must live under
// that prefix. trailingSlash gives each route a directory-style URL that
// maps 1:1 onto the exported files (route "/" -> out/index.html).
const shared: NextConfig = {
  basePath: "/app",
  trailingSlash: true,
  reactStrictMode: true,
};

// Dev runs `next dev` on :3000 while the daemon serves the API on :8666
// (Procfile.dev / `just web-dev`); the rewrite proxies /api/* across.
// Rewrites are incompatible with `output: "export"`, so the export mode
// and the proxy are split by build phase — the exported bundle calls
// /api/* same-origin and needs no rewrite.
const config = (phase: string): NextConfig => {
  if (phase === PHASE_DEVELOPMENT_SERVER) {
    return {
      ...shared,
      rewrites: async () => [
        {
          source: "/api/:path*",
          destination: "http://127.0.0.1:8666/api/:path*",
          basePath: false,
        },
      ],
    };
  }
  return { ...shared, output: "export" };
};

export default config;
