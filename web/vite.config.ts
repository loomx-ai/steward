import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";
import { isTrustedDevelopmentApiRequest } from "./dev-auth-boundary.mjs";

const devProxyToken = process.env.STEWARD_DEV_PROXY_TOKEN?.trim();

const developmentAuthBoundary: Plugin = {
  name: "steward-development-auth-boundary",
  configureServer(server) {
    if (!devProxyToken) return;
    server.middlewares.use("/api", (request, response, next) => {
      if (isTrustedDevelopmentApiRequest(request.headers)) {
        next();
        return;
      }
      response.statusCode = 403;
      response.setHeader("Content-Type", "application/json");
      response.end('{"error":"cross-origin development API request denied"}\n');
    });
  },
};

export default defineConfig({
  plugins: [react(), tailwindcss(), developmentAuthBoundary],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: true,
  },
  server: {
    port: 5858,
    strictPort: true,
    headers: {
      "X-Frame-Options": "DENY",
      "Content-Security-Policy": "frame-ancestors 'none'",
    },
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8585",
        headers: devProxyToken
          ? { Authorization: `Bearer ${devProxyToken}` }
          : undefined,
      },
    },
  },
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Library code changes far less often than the app, so it keeps its
        // cached hash across releases.
        manualChunks(id) {
          if (
            /[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler|@tanstack)[\\/]/.test(
              id,
            )
          ) {
            return "vendor";
          }
        },
      },
    },
  },
});
