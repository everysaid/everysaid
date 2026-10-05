import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { VitePWA } from "vite-plugin-pwa";
import { fileURLToPath, URL } from "node:url";

// The app is served by `chronika serve` (web/dist). In development, `pnpm dev` proxies /api to it:
// start the server with CHRONIKA_EXTRA_ORIGINS=http://localhost:5173 so passkeys work there too.
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    VitePWA({
      strategies: "injectManifest",
      srcDir: "src",
      filename: "sw.ts",
      registerType: "prompt",
      injectRegister: false,
      manifest: {
        name: "Chronika",
        short_name: "Chronika",
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
