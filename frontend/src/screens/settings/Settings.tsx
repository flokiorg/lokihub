import { AlertCircle } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import Loading from "src/components/Loading";
import { MultiRelayInput } from "src/components/MultiRelayInput";
import { ServiceCardSelector, ServiceOption } from "src/components/ServiceCardSelector";
import { ServiceConfigurationHeader } from "src/components/ServiceConfigurationHeader";
import SettingsHeader from "src/components/SettingsHeader";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Label } from "src/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { Switch } from "src/components/ui/switch";
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
import { validateHTTPURL, validateMessageBoardURL, validateWebSocketURL } from "src/utils/validation";

function Settings() {
  const { theme, darkMode, setTheme, setDarkMode } = useTheme();

  const [fiatCurrencies, setFiatCurrencies] = useState<[string, string][]>([]);

  const { data: info, mutate: reloadInfo } = useInfo();
  /* Community Options State */
  const [communityOptions, setCommunityOptions] = useState<{
    swap: ServiceOption[];
    relay: ServiceOption[];
    messageboard: ServiceOption[];
    mempool: ServiceOption[];
  }>({
    swap: [],
    relay: [],
    messageboard: [],
    mempool: [],
  });

  const errorRef = useRef<HTMLDivElement>(null);



  const [swapServiceUrl, setSwapServiceUrl] = useState("");
  const [messageboardNwcUrl, setMessageboardNwcUrl] = useState("");
  const [relay, setRelay] = useState("");
  const [mempoolApi, setMempoolApi] = useState("");
  const [enableSwap, setEnableSwap] = useState(true);
  const [enableMessageboardNwc, setEnableMessageboardNwc] = useState(true);

  // Track changes for Save button
  const [servicesDirty, setServicesDirty] = useState(false);
  const [savingServices, setSavingServices] = useState(false);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);

  useEffect(() => {
    if (info) {
      setSwapServiceUrl(info.swapServiceUrl || "");
      setMessageboardNwcUrl(info.messageboardNwcUrl || "");
      setRelay(info.relay || "");
      setMempoolApi(info.mempoolUrl || "");
      setEnableSwap(info.enableSwap ?? true);
      setEnableMessageboardNwc(info.enableMessageboardNwc ?? true);
    }
  }, [info]);

  useEffect(() => {
    async function fetchServices() {
        try {
            const services = await request<any>("/api/setup/config");
            if (services) {
                setCommunityOptions({
                    swap: services.swap_service || [],
                    relay: services.nostr_relay || [],
                    messageboard: services.messageboard_nwc || [],
                    mempool: services.flokicoin_explorer || [],
                });
            }
        } catch (error) {
            console.error(error);
            toast.error("Failed to fetch community services");
        }
    }
    fetchServices();
  }, []);

  useEffect(() => {
    async function fetchServices() {
        try {
            const services = await request<any>("/api/setup/config");
            if (services) {
                setCommunityOptions({
                    swap: services.swap_service || [],
                    relay: services.nostr_relay || [],
                    messageboard: services.messageboard_nwc || [],
                    mempool: services.flokicoin_explorer || [],
                });
            }
        } catch (error) {
            console.error(error);
            toast.error("Failed to fetch community services");
        }
    }
    fetchServices();
  }, []);

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

  // Track dirty state for services
  useEffect(() => {
    if (!info) return;
    const hasChanges =
      relay !== (info.relay || "") ||
      mempoolApi !== (info.mempoolUrl || "") ||
      swapServiceUrl !== (info.swapServiceUrl || "") ||
      messageboardNwcUrl !== (info.messageboardNwcUrl || "") ||
      enableSwap !== (info.enableSwap ?? true) ||
      enableMessageboardNwc !== (info.enableMessageboardNwc ?? true);
    setServicesDirty(hasChanges);
    if (!hasChanges) {
      setValidationErrors([]);
    }
  }, [info, relay, mempoolApi, swapServiceUrl, messageboardNwcUrl, enableSwap, enableMessageboardNwc]);

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

  async function saveServices() {
    setSavingServices(true);
    setValidationErrors([]);
    const errors: string[] = [];

    try {
      // Validate relay URLs
      if (!relay) {
        errors.push("At least one relay is required");
      } else {
        const relays = relay.split(",").map(r => r.trim()).filter(r => r.length > 0);
        if (relays.length === 0) {
             errors.push("At least one relay is required");
        }
        for (const relayUrl of relays) {
          const relayErr = validateWebSocketURL(relayUrl, "Nostr Relay URL");
          if (relayErr) {
            errors.push(relayErr);
          }
        }
      }

      // Validate mempool URL
      if (mempoolApi) {
        const mempoolErr = validateHTTPURL(mempoolApi, "Flokicoin Explorer URL");
        if (mempoolErr) errors.push(mempoolErr);
      }

      // Validate swap URL
      if (enableSwap) {
          if (!swapServiceUrl) {
              errors.push("Swap Service URL is required when enabled");
          } else {
             const swapErr = validateHTTPURL(swapServiceUrl, "Swap Service URL");
             if (swapErr) errors.push(swapErr);
          }
      }

      // Validate messageboard NWC
      if (enableMessageboardNwc) {
          if (!messageboardNwcUrl) {
              errors.push("Messageboard NWC URL is required when enabled");
          } else {
              const mbErr = validateMessageBoardURL(messageboardNwcUrl);
              if (mbErr) errors.push(mbErr);
          }
      }

      if (errors.length > 0) {
        setValidationErrors(errors);
        setSavingServices(false);
        // Scroll to error container
        setTimeout(() => {
          errorRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
        }, 100);
        return;
      }

      // All valid, save
      await updateSettings({
        relay,
        mempoolApi,
        swapServiceUrl,
        messageboardNwcUrl,
        enableSwap,
        enableMessageboardNwc,
      }, "Services updated successfully", "Failed to update services");
      
      setServicesDirty(false);
    } finally {
      setSavingServices(false);
    }
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

        {/* Services Section */}
        <div className="space-y-4">
          <h3 className="text-xl font-medium">Services</h3>
          <div className="space-y-4">
              <ServiceConfigurationHeader />

            {/* Flokicoin Explorer */}
            {/* Flokicoin Explorer */}
            {/* Flokicoin Explorer */}
            <Card>
                <CardHeader>
                    <CardTitle className="text-base">Flokicoin Explorer</CardTitle>
                    <CardDescription>
                        The URL for the Flokicoin Explorer API. This provides fee estimates and transaction details.
                    </CardDescription>
                </CardHeader>
                <CardContent>
                  <ServiceCardSelector
                    value={mempoolApi}
                    onChange={setMempoolApi}
                    options={communityOptions.mempool}
                    placeholder="https://..."
                  />
                </CardContent>
            </Card>

            {/* Relay */}
            {/* Relay */}
            {/* Relay */}
            <Card>
                <CardHeader>
                    <CardTitle className="text-base">Nostr Relays</CardTitle>
                    <CardDescription>
                        Connect to multiple Nostr relays for Wallet Connect (NWC) communication. Multiple relays improve availability.
                    </CardDescription>
                </CardHeader>
                <CardContent>
                  <MultiRelayInput
                    value={relay}
                    onChange={setRelay}
                    options={communityOptions.relay}
                    placeholder="wss://..."
                  />
                </CardContent>
            </Card>

            {/* Swap Service URL */}
            {/* Swap Service URL */}
            {/* Swap Service URL */}
            <Card>
                <CardHeader className="flex flex-row items-start justify-between space-y-0 pb-2">
                    <div className="space-y-1">
                        <CardTitle className="text-base">Swap Service URL</CardTitle>
                        <CardDescription>
                            The swap service enables atomic swaps between Lightning and on-chain Flokicoin.
                        </CardDescription>
                    </div>
                    <div className="flex items-center space-x-2">
                        <Label htmlFor="enableSwap" className="text-sm font-normal">Enable</Label>
                        <Switch
                            id="enableSwap"
                            checked={enableSwap}
                            onCheckedChange={setEnableSwap}
                        />
                    </div>
                </CardHeader>
                {enableSwap && (
                    <CardContent>
                        <ServiceCardSelector
                            value={swapServiceUrl}
                            onChange={setSwapServiceUrl}
                            options={communityOptions.swap}
                            placeholder="https://..."
                        />
                    </CardContent>
                )}
            </Card>

            

            {/* Messageboard NWC URL */}
            {/* Messageboard NWC URL */}
            {/* Messageboard NWC URL */}
            <Card>
                <CardHeader className="flex flex-row items-start justify-between space-y-0 pb-2">
                    <div className="space-y-1">
                        <CardTitle className="text-base">Messageboard NWC URL</CardTitle>
                        <CardDescription>
                             The Nostr Wallet Connect URL for the Lightning messageboard widget.
                        </CardDescription>
                    </div>
                    <div className="flex items-center space-x-2">
                        <Label htmlFor="enableMessageboardNwc" className="text-sm font-normal">Enable</Label>
                        <Switch
                            id="enableMessageboardNwc"
                            checked={enableMessageboardNwc}
                            onCheckedChange={setEnableMessageboardNwc}
                        />
                    </div>
                </CardHeader>
                {enableMessageboardNwc && (
                    <CardContent>
                        <ServiceCardSelector
                            value={messageboardNwcUrl}
                            onChange={setMessageboardNwcUrl}
                            options={communityOptions.messageboard}
                            placeholder="nostr+walletconnect://..."
                        />
                    </CardContent>
                )}
            </Card>

            {validationErrors.length > 0 && (
                <div ref={errorRef} className="scroll-mt-4">
                  <Alert variant="destructive" className="mt-4 w-full animate-in fade-in slide-in-from-bottom-2">
                      <AlertCircle className="h-4 w-4" />
                      <AlertTitle>Configuration Errors</AlertTitle>
                      <AlertDescription>
                          <ul className="list-disc pl-5 space-y-1 mt-2">
                              {validationErrors.map((err, i) => (
                                  <li key={i}>{err}</li>
                              ))}
                          </ul>
                      </AlertDescription>
                  </Alert>
                </div>
            )}

            {/* Save Services Button */}
            <div className="flex justify-end">
              <Button
                type="button"
                onClick={saveServices}
                disabled={!servicesDirty || savingServices}
              >
                {savingServices ? "Saving..." : "Save Services"}
              </Button>
            </div>

          </div>
        </div>
      </form>
    </>
  );
}

export default Settings;
