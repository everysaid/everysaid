import { defineConfig, type Plugin } from "vite";
import { readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { VitePWA } from "vite-plugin-pwa";
import { fileURLToPath, URL } from "node:url";

// The emoji picker's data (emojibase, English), served by the app itself at /emojibase/en/: no
// request leaves for a CDN (the server's Content-Security-Policy would not allow one either).
function emojibase(): Plugin {
  const files = ["data.json", "messages.json"];
  const read = (f: string) => readFileSync(fileURLToPath(new URL(`./node_modules/emojibase-data/en/${f}`, import.meta.url)));
  return {
    name: "emojibase",
    configureServer(server) {
      server.middlewares.use("/emojibase/en/", (req, res, next) => {
        const f = (req.url ?? "").replace(/^\//, "").split("?")[0];
        if (!files.includes(f)) return next();
        res.setHeader("Content-Type", "application/json");
        res.end(read(f));
      });
    },
    generateBundle() {
      for (const f of files) this.emitFile({ type: "asset", fileName: `emojibase/en/${f}`, source: read(f) });
    },
  };
}

// The app is served by `everysaid serve` (web/dist). In development, `pnpm dev` proxies /api to it:
// start the server with EVERYSAID_EXTRA_ORIGINS=http://localhost:5173 so passkeys work there too.
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    emojibase(),
    VitePWA({
      strategies: "injectManifest",
      srcDir: "src",
      filename: "sw.ts",
      registerType: "prompt",
      injectRegister: false,
      manifest: {
        name: "Everysaid",
        short_name: "Everysaid",
        description: "Όλες οι συνομιλίες σου σε ένα μέρος",
        start_url: "/",
        scope: "/",
        display: "standalone",
        background_color: "#0b0d12",
        theme_color: "#0b0d12",
        icons: [
          { src: "/icon-192.png", sizes: "192x192", type: "image/png" },
          { src: "/icon-512.png", sizes: "512x512", type: "image/png" },
          { src: "/icon-maskable.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
        ],
      },
      injectManifest: { globPatterns: ["**/*.{js,css,html,svg,png,woff2}"] },
      devOptions: { enabled: false },
    }),
  ],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    port: 5173,
    proxy: { "/api": { target: "http://localhost:8520", ws: true, changeOrigin: false } },
  },
  build: { target: "es2022", chunkSizeWarningLimit: 900 },
});
