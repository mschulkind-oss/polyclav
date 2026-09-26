import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

// The app is served by the polyclav daemon from /app/ (see
// internal/web/static.go), so every page and asset URL must live under
// that prefix. trailingSlash gives each route a directory-style URL that
// maps 1:1 onto the exported files (route "/" -> .next-build/index.html).
const shared: NextConfig = {
  basePath: "/app",
  trailingSlash: true,
  reactStrictMode: true,
};

// Standalone `just web-dev` runs `next dev` on :3000 while the daemon serves
// the API on :8666; `just dev` runs the same Next dev server under Hivemind,
// which injects PORT=5100 for the web process. In both cases the rewrite
// proxies /api/* across. Rewrites are incompatible with `output: "export"`,
// so export mode and the proxy are split by build phase — the exported bundle
// calls /api/* same-origin and needs no rewrite.
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
  // Constant build id: `next build` generates a random one per run, which
  // churns _next/static/<id>/ + the manifests + every HTML page on every
  // rebuild — dirtying internal/web/static/app (a committed build
  // artifact) even when the sources didn't change, and making CI's
  // export-freshness gate impossible to satisfy. Asset cache-busting
  // still comes from the content-hashed chunk filenames.
  return {
    ...shared,
    distDir: ".next-build",
    output: "export",
    generateBuildId: () => "embedded",
  };
};

export default config;
