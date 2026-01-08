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
import { Switch } from "src/components/ui/switch";
import { useInfo } from "src/hooks/useInfo";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";
import { validateHTTPURL, validateMessageBoardURL, validateWebSocketURL } from "src/utils/validation";

export function Services() {
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
      if (!mempoolApi) {
        errors.push("Flokicoin Explorer URL is required");
      } else {
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
        title="Services"
        description="Configure external services and connections."
      />
      <form className="w-full flex flex-col gap-8">
        {/* Services Section */}
        <div className="space-y-4">
          <ServiceConfigurationHeader />

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
      </form>
    </>
  );
}
