import { App } from "src/types";

export type AppStoreApp = {
  id: string;
  title: string;
  description: string;
  extendedDescription: string;

  logo?: string;
  categories: (keyof typeof appStoreCategories)[];

  // General links
  webLink?: string;

  // App store links
  playLink?: string;
  appleLink?: string;
  zapStoreLink?: string;

  // Extension store links
  chromeLink?: string;
  firefoxLink?: string;

  installGuide?: React.ReactNode;
  finalizeGuide?: React.ReactNode;
  hideConnectionQr?: boolean;
  internal?: boolean;
  superuser?: boolean;
};

export const appStoreCategories = {
  "wallet-interfaces": {
    title: "Wallet Interfaces",
    priority: 1,
  },
  "social-media": {
    title: "Social Media",
    priority: 2,
  },
  ai: {
    title: "AI",
    priority: 20,
  },
  "merchant-tools": {
    title: "Merchant Tools",
    priority: 10,
  },
  music: {
    title: "Music",
    priority: 20,
  },
  blogging: {
    title: "Blogging",
    priority: 20,
  },
  "payment-tools": {
    title: "Payment Tools",
    priority: 10,
  },
  shopping: {
    title: "Shopping",
    priority: 30,
  },
  "nostr-tools": {
    title: "Nostr Tools",
    priority: 40,
  },
  games: {
    title: "Games",
    priority: 50,
  },
  misc: {
    title: "Misc",
    priority: 100,
  },
} as const;

export const sortedAppStoreCategories = Object.entries(appStoreCategories).sort(
  (a, b) => a[1].priority - b[1].priority
);

export const appStoreApps: AppStoreApp[] = (
  [
    
  ] satisfies AppStoreApp[]
)

// .sort((a, b) => (a.title.toUpperCase() > b.title.toUpperCase() ? 1 : -1));

export const getAppStoreApp = (app: App) => {
  return appStoreApps.find(
    (suggestedApp) =>
      suggestedApp.id === (app.metadata?.app_store_app_id ?? "") ||
      app.name.includes(suggestedApp.title)
  );
};
