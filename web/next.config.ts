import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /* config options here */
  // Standalone output for the container image (web/Dockerfile copies .next/standalone and runs
  // `node server.js` on Fly). Keep this on — the non-standalone build needs the full Next runtime
  // plus node_modules in the image.
  output: "standalone",
  allowedDevOrigins: [
    "192.168.0.111"
  ]
};

export default nextConfig;
