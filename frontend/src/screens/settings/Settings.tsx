import { useEffect, useState } from "react";
import { toast } from "sonner";
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
  FLOKICOIN_DISPLAY_FORMAT_BIP177,
  FLOKICOIN_DISPLAY_FORMAT_LOKI,
} from "src/constants";
import { useInfo } from "src/hooks/useInfo";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";


function Settings() {
  const { theme, darkMode, setTheme, setDarkMode } = useTheme();

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
        toast.error(`Failed to fetch currencies: ${errorMessage}`);
      }
    }

    fetchCurrencies();
  }, []);



  async function updateSettings(
    payload: Record<string, string | boolean>,
    successMessage: string,
    errorMessage: string
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
      handleRequestError(errorMessage, error);
    }
  }

  async function updateCurrency(currency: string) {
    await updateSettings(
      { currency },
      `Currency set to ${currency}`,
      "Failed to update currencies"
    );
  }

  async function updateFlokicoinDisplayFormat(flokicoinDisplayFormat: string) {
    await updateSettings(
      { flokicoinDisplayFormat },
      "Flokicoin display format updated",
      "Failed to update flokicoin display format"
    );
  }



  if (!info) {
    return <Loading />;
  }


  return (
    <>
      <SettingsHeader
        title="General"
        description="General Lokihub settings."
      />
      <form className="w-full flex flex-col gap-8">
        {/* Theme & Appearance Section */}
        <div className="space-y-4">
          <h3 className="text-xl font-medium">Appearance</h3>
          <div className="space-y-4">
            <div className="grid gap-2">
              <Label htmlFor="theme">Theme</Label>
              <Select
                value={theme}
                onValueChange={(value) => {
                  setTheme(value as Theme);
                  toast("Theme updated.");
                }}
              >
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder="Theme" />
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
              <Label htmlFor="appearance">Appearance</Label>
              <Select
                value={darkMode}
                onValueChange={(value) => {
                  setDarkMode(value as DarkMode);
                  toast("Appearance updated.");
                }}
              >
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder="Appearance" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="system">System</SelectItem>
                  <SelectItem value="light">Light</SelectItem>
                  <SelectItem value="dark">Dark</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        </div>

        {/* Units & Currency Section */}
        <div className="space-y-4">
          <h3 className="text-xl font-medium">Units & Currency</h3>
          <div className="space-y-4">
            <div className="grid gap-1.5">
              <Label htmlFor="flokicoinDisplayFormat">Display Unit</Label>
              <Select
                value={info.flokicoinDisplayFormat}
                onValueChange={updateFlokicoinDisplayFormat}
              >
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder="Select a display format" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={FLOKICOIN_DISPLAY_FORMAT_BIP177}>
                    FLC
                  </SelectItem>
                  <SelectItem value={FLOKICOIN_DISPLAY_FORMAT_LOKI}>
                    loki
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="currency">Fiat Currency</Label>
              <Select value={info?.currency} onValueChange={updateCurrency}>
                <SelectTrigger className="w-full md:w-60">
                  <SelectValue placeholder="Select a currency" />
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


      </form>
    </>
  );
}

export default Settings;
