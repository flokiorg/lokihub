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
    VitePWA({
      registerType: "autoUpdate",
      // disable service worker - Lokihub cannot be used offline (and also breaks oauth callback)
      injectRegister: false,
      includeAssets: [
        "favicon.ico",
        "robots.txt",
        "icon-192.png",
        "icon-512.png",
      ],
      useCredentials: true, // because the manifest might sit behind authentication
      manifest: {
        short_name: "Lokihub",
        name: "Lokihub",
        icons: [
          {
            src: "icon-192.png",
            sizes: "192x192",
            type: "image/png",
          },
          {
            src: "icon-512.png",
            sizes: "512x512",
            type: "image/png",
          },
          {
            src: "icon-1024.png",
            sizes: "1024x1024",
            type: "image/png",
          },
        ],
        start_url: ".",
        display: "standalone",
        theme_color: "#000000",
        background_color: "#ffffff",
      },
      workbox: {
        maximumFileSizeToCacheInBytes: 3000000, // 3MB
      },
    }),
    ...(command === "serve" ? [insertDevCSPPlugin] : []),
  ],
  server: {
    port: process.env.VITE_PORT ? parseInt(process.env.VITE_PORT) : undefined,
    proxy: {
      "/api": {
        target: process.env.VITE_API_URL || "http://127.0.0.1:8080",
        secure: false,
      },
      "/logout": {
        target: process.env.VITE_API_URL || "http://127.0.0.1:8080",
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
            connect-src 'self' wss: http://localhost:* ws://localhost:*;
            style-src 'self' 'unsafe-inline';
            img-src 'self' data: blob:;
          "
        />
        `
      );
    },
  },
};
