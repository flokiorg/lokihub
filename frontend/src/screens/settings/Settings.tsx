import { Check } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { cn } from "src/lib/utils";
import { LanguageSwitcher } from "src/components/LanguageSwitcher";
import { WithdrawNodeSettings } from "src/components/withdraw/WithdrawNodeSettings";
import Loading from "src/components/Loading";
import SettingsHeader from "src/components/SettingsHeader";

import { Label } from "src/components/ui/label";
import { Separator } from "src/components/ui/separator";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";

import {
  DarkMode,
  Theme,
  Themes,
  useTheme,
} from "src/components/ui/theme-provider";
import {
  FLOKICOIN_DISPLAY_FORMAT_AUTO,
  FLOKICOIN_DISPLAY_FORMAT_FLC,
  FLOKICOIN_DISPLAY_FORMAT_LOKI,
} from "src/constants";
import { useHubInfo } from "src/hooks/useHubInfo";
import { useInfo } from "src/hooks/useInfo";
import { FLOKICOIN_NATIVE_ASSET } from "src/utils/flokicoinFormatting";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

function Settings() {
  const { theme, darkMode, setTheme, setDarkMode } = useTheme();
  const { t } = useTranslation("settings");

  const [fiatCurrencies, setFiatCurrencies] = useState<[string, string][]>([]);

  const { data: info, mutate: reloadInfo } = useInfo();
  const { data: hubInfo } = useHubInfo();
  const asset = hubInfo?.nativeAsset ?? FLOKICOIN_NATIVE_ASSET;

  useEffect(() => {
    async function fetchCurrencies() {
      try {
        const data =
          await request<Record<string, { name: string }>>("/api/currencies");
        if (!data) {
          throw new Error("Failed to fetch currencies");
        }

        const mappedCurrencies: [string, string][] = Object.entries(data).map(
          ([code, details]) => [code.toUpperCase(), details.name]
        );

        mappedCurrencies.sort((a, b) => a[1].localeCompare(b[1]));

        setFiatCurrencies(mappedCurrencies);
      } catch (error) {
        console.error(error);
        const errorMessage =
          error instanceof Error ? error.message : "Unknown error";
        toast.error(t("toasts.currencyFetchFailed", { error: errorMessage }));
      }
    }

    fetchCurrencies();
  }, []);

  async function updateSettings(
    payload: Record<string, string | boolean>,
    successMessage: string
  ) {
    try {
      await request("/api/settings", {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(payload),
      });
      await reloadInfo();
      toast(successMessage);
    } catch (error) {
      console.error(error);
      handleRequestError(t("toasts.updateFailed"), error);
    }
  }

  async function updateCurrency(currency: string) {
    await updateSettings(
      { currency },
      t("toasts.currencyUpdated", { currency })
    );
  }

  async function updateFlokicoinDisplayFormat(flokicoinDisplayFormat: string) {
    await updateSettings({ flokicoinDisplayFormat }, t("toasts.formatUpdated"));
  }

  if (!info) {
    return <Loading />;
  }

  return (
    <>
      <SettingsHeader title={t("title")} description={t("description")} />
      <form className="w-full flex flex-col gap-6">
        {/* Card-light: each group is a heading + hairline-separated section
            (matching the Developer page), not a bordered card each. */}
        <section className="flex flex-col gap-4">
          <h3 className="text-base font-semibold">
            {t("sections.appearance")}
          </h3>
          <div className="space-y-4">
            <div className="grid gap-2">
              <Label>{t("appearance.theme")}</Label>
              <div className="grid max-w-2xl grid-cols-2 gap-3 sm:grid-cols-3">
                {Themes.map((th) => (
                  <button
                    key={th}
                    type="button"
                    onClick={() => {
                      setTheme(th as Theme);
                      toast(t("toasts.themeUpdated"));
                    }}
                    className={cn(
                      "rounded-lg border bg-card p-2 text-left transition-colors hover:border-primary/50",
                      theme === th && "border-primary ring-2 ring-primary/30"
                    )}
                  >
                    <div
                      className={cn(
                        "flex h-10 overflow-hidden rounded-md border",
                        th !== "default" && `theme-${th}`
                      )}
                    >
                      <div className="flex-1 bg-background" />
                      <div className="flex-1 bg-primary" />
                      <div className="flex-1 bg-secondary" />
                      <div className="flex-1 bg-muted" />
                    </div>
                    <div className="mt-1.5 flex items-center justify-between px-0.5">
                      <span className="text-sm font-medium capitalize">
                        {th === "default" ? t("appearance.themes.default") : th}
                      </span>
                      {theme === th && (
                        <Check className="size-4 text-primary" />
                      )}
                    </div>
                  </button>
                ))}
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="appearance">{t("appearance.darkMode")}</Label>
              <Select
                value={darkMode}
                onValueChange={(value) => {
                  setDarkMode(value as DarkMode);
                  toast(t("toasts.appearanceUpdated"));
                }}
              >
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder={t("appearance.darkMode")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="system">
                    {t("appearance.modes.system")}
                  </SelectItem>
                  <SelectItem value="light">
                    {t("appearance.modes.light")}
                  </SelectItem>
                  <SelectItem value="dark">
                    {t("appearance.modes.dark")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        </section>

        <Separator />

        <section className="flex flex-col gap-4">
          <h3 className="text-base font-semibold">
            {t("sections.unitsAndCurrency")}
          </h3>
          <div className="space-y-4">
            <div className="grid gap-1.5">
              <Label htmlFor="flokicoinDisplayFormat">
                {t("units.displayUnit")}
              </Label>
              <Select
                value={info.flokicoinDisplayFormat}
                onValueChange={updateFlokicoinDisplayFormat}
              >
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder={t("units.displayUnit")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={FLOKICOIN_DISPLAY_FORMAT_AUTO}>
                    {t("units.auto")}
                  </SelectItem>
                  <SelectItem value={FLOKICOIN_DISPLAY_FORMAT_FLC}>
                    {asset.ticker}
                  </SelectItem>
                  <SelectItem value={FLOKICOIN_DISPLAY_FORMAT_LOKI}>
                    {asset.unitName}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="currency">{t("units.fiatCurrency")}</Label>
              <Select value={info?.currency} onValueChange={updateCurrency}>
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder={t("units.fiatCurrency")} />
                </SelectTrigger>
                <SelectContent>
                  {fiatCurrencies.map(([code, name]) => (
                    <SelectItem key={code} value={code}>
                      {name} ({code})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
        </section>

        <Separator />

        <section className="flex flex-col gap-4">
          <h3 className="text-base font-semibold">{t("sections.language")}</h3>
          <LanguageSwitcher />
        </section>

        <Separator />

        <section className="flex flex-col gap-4">
          <h3 className="text-base font-semibold">{t("sections.payouts")}</h3>
          <WithdrawNodeSettings />
        </section>
      </form>
    </>
  );
}

export default Settings;
