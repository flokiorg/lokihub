import { describe, it, expect } from "vitest";
import i18next from "i18next";

// Acceptance: the {{chain}} token (set by useNativeAssetI18n from the hub's
// native asset name) flips every chain-name string with no code change — the
// same global-defaultVariables mechanism it uses at runtime.
describe("native-asset {{chain}} interpolation", () => {
  it("renders {{chain}} from the default variables and flips when the asset name changes", async () => {
    const i18n = i18next.createInstance();
    await i18n.init({
      lng: "en",
      resources: {
        en: {
          translation: {
            deposit: "Deposit {{chain}}",
            pay: "{{chain}} payment of {{amount}}",
          },
        },
      },
      interpolation: { escapeValue: false, defaultVariables: { chain: "Flokicoin" } },
    });
    // The app augments i18next's key types; this fresh instance uses ad-hoc keys.
    const t = i18n.t as unknown as (k: string, o?: Record<string, unknown>) => string;
    expect(t("deposit")).toBe("Deposit Flokicoin");

    // useNativeAssetI18n updates both the options and the live interpolator copy.
    i18n.options.interpolation!.defaultVariables = { chain: "Bitcoin" };
    (i18n.services.interpolator as unknown as { defaultVariables: Record<string, string> }).defaultVariables = { chain: "Bitcoin" };
    expect(t("deposit")).toBe("Deposit Bitcoin");
    // explicit variables still interpolate alongside the default
    expect(t("pay", { amount: "5" })).toBe("Bitcoin payment of 5");
  });
});
