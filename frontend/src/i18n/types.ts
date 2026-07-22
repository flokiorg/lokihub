import enCommon from "./locales/en/common.json";
import enHome from "./locales/en/home.json";
import enSettings from "./locales/en/settings.json";
import enWallet from "./locales/en/wallet.json";
import enSetup from "./locales/en/setup.json";
import enChannels from "./locales/en/channels.json";
import enApps from "./locales/en/apps.json";
import enHelp from "./locales/en/help.json";
import enCircles from "./locales/en/circles.json";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "common";
    resources: {
      common: typeof enCommon;
      home: typeof enHome;
      settings: typeof enSettings;
      wallet: typeof enWallet;
      setup: typeof enSetup;
      channels: typeof enChannels;
      apps: typeof enApps;
      help: typeof enHelp;
      circles: typeof enCircles;
    };
  }
}
