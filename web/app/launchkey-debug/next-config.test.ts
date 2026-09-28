import { PHASE_DEVELOPMENT_SERVER } from "next/constants";
import { describe, expect, it } from "vitest";
import config from "../../next.config";

describe("development SSE proxy", () => {
  it("does not gzip long-lived event streams", async () => {
    const dev = config(PHASE_DEVELOPMENT_SERVER);
    expect(dev.compress).toBe(false);
    expect(await dev.rewrites?.()).toContainEqual({
      source: "/api/:path*",
      destination: "http://127.0.0.1:8666/api/:path*",
      basePath: false,
    });
  });
});
