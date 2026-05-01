import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { LanguageSwitcher } from "src/components/LanguageSwitcher";
import Loading from "src/components/Loading";
import SettingsHeader from "src/components/SettingsHeader";

import { Label } from "src/components/ui/label";
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
import { useInfo } from "src/hooks/useInfo";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";


function Settings() {
  const { theme, darkMode, setTheme, setDarkMode } = useTheme();
  const { t } = useTranslation("settings");

  const [fiatCurrencies, setFiatCurrencies] = useState<[string, string][]>([]);

  const { data: info, mutate: reloadInfo } = useInfo();








  useEffect(() => {
    async function fetchCurrencies() {
      try {
        const data = await request<Record<string, { name: string }>>("/api/currencies");
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
        const errorMessage = error instanceof Error ? error.message : "Unknown error";
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
    await updateSettings(
      { flokicoinDisplayFormat },
      t("toasts.formatUpdated")
    );
  }



  if (!info) {
    return <Loading />;
  }


  return (
    <>
      <SettingsHeader
        title={t("title")}
        description={t("description")}
      />
      <form className="w-full flex flex-col gap-8">
        {/* Theme & Appearance Section */}
        <div className="space-y-4">
          <h3 className="text-xl font-medium">{t("sections.appearance")}</h3>
          <div className="space-y-4">
            <div className="grid gap-2">
              <Label htmlFor="theme">{t("appearance.theme")}</Label>
              <Select
                value={theme}
                onValueChange={(value) => {
                  setTheme(value as Theme);
                  toast(t("toasts.themeUpdated"));
                }}
              >
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder={t("appearance.theme")} />
                </SelectTrigger>
                <SelectContent>
                  {Themes.map((theme) => (
                    <SelectItem key={theme} value={theme}>
                      <span className="capitalize">{theme}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
                  <SelectItem value="system">{t("appearance.modes.system")}</SelectItem>
                  <SelectItem value="light">{t("appearance.modes.light")}</SelectItem>
                  <SelectItem value="dark">{t("appearance.modes.dark")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        </div>

        {/* Units & Currency Section */}
        <div className="space-y-4">
          <h3 className="text-xl font-medium">{t("sections.unitsAndCurrency")}</h3>
          <div className="space-y-4">
            <div className="grid gap-1.5">
              <Label htmlFor="flokicoinDisplayFormat">{t("units.displayUnit")}</Label>
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
                    FLC
                  </SelectItem>
                  <SelectItem value={FLOKICOIN_DISPLAY_FORMAT_LOKI}>
                    loki
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
        </div>

        {/* Language Section */}
        <div className="space-y-4">
          <h3 className="text-xl font-medium">{t("sections.language")}</h3>
          <div className="space-y-4">
            <LanguageSwitcher />
          </div>
        </div>


      </form>
    </>
  );
}

export default Settings;
