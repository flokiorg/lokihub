import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react-swc";
import path from "path";
import { defineConfig, Plugin } from "vite";
import { VitePWA } from "vite-plugin-pwa";
import svgr from "vite-plugin-svgr";

const svgrConfig = {
  svgrOptions: {
    svgo: true,
    svgoConfig: {
      plugins: [
        {
          name: 'preset-default',
          params: {
            overrides: {
              removeViewBox: false,
            },
          },
        },
        'removeDimensions',
      ],
    },
  },
};

export default defineConfig(({ command }) => ({
  plugins: [
    react(),
    svgr(svgrConfig),
    tailwindcss(),
    // tsconfigPaths(),
    ...(command === "serve" ? [insertDevCSPPlugin] : []),
    VitePWA({
      registerType: "autoUpdate",
      // site.webmanifest is already a static file linked in index.html —
      // don't have the plugin generate a second one.
      manifest: false,
      workbox: {
        // Only runtime-cache images (Nostr profile pictures) — do NOT
        // app-shell-precache the build output. This app's own JS bundle
        // exceeds Workbox's default 2 MiB precache limit, and full-app
        // offline precaching isn't the goal here anyway; an empty glob
        // disables precache-manifest generation entirely.
        globPatterns: [],
        runtimeCaching: [
          {
            // Nostr profile picture URLs are arbitrary hosts (any npub's
            // kind:0 "picture" field), so this matches by request type
            // rather than a URL pattern.
            urlPattern: ({ request }) => request.destination === "image",
            handler: "CacheFirst",
            options: {
              cacheName: "nostr-profile-images",
              expiration: {
                maxEntries: 300,
                maxAgeSeconds: 60 * 60 * 24 * 30, // 30 days
              },
              cacheableResponse: { statuses: [0, 200] },
            },
          },
        ],
      },
    }),
  ],
  server: {
    port: process.env.VITE_PORT ? parseInt(process.env.VITE_PORT) : undefined,
    // Without an explicit host, Vite's default bind can land on IPv6-only
    // loopback ([::1]) depending on the Node/OS resolver — observed doing
    // exactly that here. Wails' dev-mode asset proxy (ExternalAssetHandler)
    // discovers this server by the "localhost" string Vite prints and
    // reverse-proxies to it; when that resolves to an address nothing is
    // actually listening on, proxied requests fail with connection resets/502s,
    // which is fatal for `wails dev` (the whole frontend is served through
    // this proxy). Pin to 127.0.0.1 explicitly so it's unambiguous — except
    // in the Docker dev stack (docker-compose.dev.yml), which needs Vite
    // reachable from outside the container (via Caddy) rather than loopback-only.
    host: process.env.VITE_USE_POLLING === "true" ? undefined : "127.0.0.1",
    // `wails dev` opens the native window the instant this server logs
    // "ready" and then immediately requests this app's entire module graph
    // at once (Vite serves unbundled ESM in dev — one request per file, and
    // this app has a lot of them). Vite is single-threaded and transforms
    // each file synchronously on first request, so that burst can outrun its
    // accept queue and get connection resets (surfaced as 502s by Wails'
    // proxy, which has no retry — and since ESM import failures are fatal,
    // even one reset blanks the whole page). Warming these up compiles and
    // caches them in the background during the several-second gap between
    // Vite's "ready" and the Go app actually finishing its own build/launch,
    // so the real burst mostly hits cache instead of the transform pipeline.
    warmup: {
      clientFiles: ["./src/**/*.{ts,tsx}"],
    },
    // Bind-mounted source (docker-compose.dev.yml's frontend service) very
    // often doesn't propagate inotify events into the container, so
    // chokidar's default watcher silently never fires HMR there. Polling
    // opts in only when explicitly requested (set in the Docker dev
    // environment), leaving native `yarn dev` on the host using the
    // cheaper event-driven watcher, which already works fine there.
    watch: process.env.VITE_USE_POLLING === "true"
      ? { usePolling: true, interval: 300 }
      : undefined,
    proxy: {
      // VITE_API_URL (Docker dev: points at the `backend` container) wins
      // when set; otherwise this derives from the same PORT the Go backend
      // itself reads (config/models.go's envconfig:"PORT"), so setting PORT
      // once in .env is enough to move both sides off a colliding port.
      "/api": {
        target:
          process.env.VITE_API_URL ||
          `http://127.0.0.1:${process.env.PORT || 1610}`,
        secure: false,
      },
      "/logout": {
        target:
          process.env.VITE_API_URL ||
          `http://127.0.0.1:${process.env.PORT || 1610}`,
        secure: false,
      },
    },
  },
  resolve: {
    preserveSymlinks: true,
    alias: {
      src: path.resolve(__dirname, "./src"),
      wailsjs: path.resolve(__dirname, "./wailsjs"),
      // used to refrence public assets when importing images or other
      // assets from the public folder
      // this is necessary to inject the base path during build
      public: "",
      // @nostr-dev-kit/ndk uses tseep's EventEmitter pervasively (NDKRelay,
      // NDKSubscription, NDK itself, ...). tseep's default build "bakes"
      // fast dispatchers via `eval`, which our CSP's `script-src 'self'`
      // (no unsafe-eval) blocks — breaking relay connect/subscription
      // events at runtime. tseep/lib/fallback probes eval availability
      // once and transparently swaps in its no-eval EventEmitter when
      // blocked, so this alias fixes it for every transitive import
      // without loosening the CSP.
      tseep: "tseep/lib/fallback",
    },
  },
  build: {
    assetsInlineLimit: 0,
  },
  html:
    command === "serve"
      ? {
          cspNonce: "DEVELOPMENT",
        }
      : undefined,
  base: process.env.BASE_PATH || "/",
}));

const DEVELOPMENT_NONCE = "'nonce-DEVELOPMENT'";

const insertDevCSPPlugin: Plugin = {
  name: "dev-csp",
  transformIndexHtml: {
    order: "pre",
    handler: (html) => {
      return html.replace(
        "<head>",
        `<head>
        <!-- DEV-ONLY CSP - when making changes here, also update the CSP header in http_service.go (without the nonce!) -->
        <meta
          http-equiv="Content-Security-Policy"
          content="
            default-src 'self' 'unsafe-inline';
            script-src 'self' 'unsafe-inline' 'unsafe-eval' ${DEVELOPMENT_NONCE};
            connect-src 'self' https: wss: http://localhost:* ws://localhost:* ws://wails.localhost:*;
            style-src 'self' 'unsafe-inline';
            img-src 'self' data: blob:;
          "
        />
        `
      );
    },
  },
};
