import { AlertCircle, RotateCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { LSPManagementCard } from "src/components/LSPManagementCard";
import { MultiRelayInput } from "src/components/MultiRelayInput";
import { ServiceCardSelector, ServiceOption } from "src/components/ServiceCardSelector";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardDescription,
    CardFooter,
    CardHeader,
    CardTitle
} from "src/components/ui/card";
import { Label } from "src/components/ui/label";
import { Switch } from "src/components/ui/switch";
import { LSP } from "src/types";
import { request } from "src/utils/request";
import { validateHTTPURL, validateMessageBoardURL, validateWebSocketURL } from "src/utils/validation";

type GlobalErrorProps = {
  message: string;
};

export function GlobalError({ message }: GlobalErrorProps) {
  const errorRef = useRef<HTMLDivElement>(null);
  const [loading, setLoading] = useState(false);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);
  const [services, setServices] = useState({
    swapServiceUrl: "",
    messageboardNwcUrl: "",
    relay: "",
    mempoolApi: "",
    enableSwap: true,
    enableMessageboardNwc: true,
    lsps: [] as LSP[],
  });
  const [showConfig, setShowConfig] = useState(false);

  const [loadingConfig, setLoadingConfig] = useState(false);
  const [communityOptions, setCommunityOptions] = useState<{
    swap: ServiceOption[];
    relay: ServiceOption[];
    messageboard: ServiceOption[];
    mempool: ServiceOption[];
    lsp: ServiceOption[];
  }>({
    swap: [],
    relay: [],
    messageboard: [],
    mempool: [],
    lsp: [],
  });

  useEffect(() => {
    if (showConfig) {
      setLoadingConfig(true);
      request<import("src/types").InfoResponse>("/api/info")
        .then((info) => {
          if (info) {
            setServices({
              swapServiceUrl: info.swapServiceUrl || "",
              messageboardNwcUrl: info.messageboardNwcUrl || "",
              relay: info.relay || "",
              mempoolApi: info.mempoolUrl || "",
              enableSwap: info.enableSwap ?? true,
              enableMessageboardNwc: info.enableMessageboardNwc ?? true,
              lsps: info.lsps || [],
            });
          }
        })
        .catch((e) => {
          console.error("Failed to fetch info", e);
          toast.error("Failed to load current configuration");
        })
        .finally(() => {
          setLoadingConfig(false);
        });

      request<any>("/api/setup/config")
        .then((services) => {
            if (services) {
                setCommunityOptions({
                    swap: services.swap_service || [],
                    relay: services.nostr_relay || [],
                    messageboard: services.messageboard_nwc || [],
                    mempool: services.flokicoin_explorer || [],
                    lsp: services.lsps || [],
                });
            }
        })
        .catch(console.error);
    }
  }, [showConfig]);

  const saveConfiguration = async () => {
      setLoading(true);
      setValidationErrors([]);
      const errors: string[] = [];

      try {
        if (!services.relay) {
            errors.push("At least one relay is required");
        } else {
           const relays = services.relay.split(",").map(r => r.trim()).filter(r => r.length > 0);
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
        
        if (!services.mempoolApi) {
            errors.push("Flokicoin Explorer URL is required");
        } else {
            const mempoolErr = validateHTTPURL(services.mempoolApi, "Flokicoin Explorer URL");
            if (mempoolErr) errors.push(mempoolErr);
        }

        if (services.enableSwap) {
             if (!services.swapServiceUrl) {
                 errors.push("Swap Service URL is required when enabled");
             } else {
                  const swapErr = validateHTTPURL(services.swapServiceUrl, "Swap Service URL");
                  if (swapErr) errors.push(swapErr);
             }
        }

        if (services.enableMessageboardNwc) {
             if (!services.messageboardNwcUrl) {
                 errors.push("Messageboard NWC URL is required when enabled");
             } else {
                 const mbErr = validateMessageBoardURL(services.messageboardNwcUrl);
                 if (mbErr) errors.push(mbErr);
             }
        }

        if (errors.length > 0) {
            setValidationErrors(errors);
            setLoading(false);
            setTimeout(() => {
                errorRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
            }, 100);
            return;
        }

        await Promise.all([
          request("/api/settings", {
            method: "PUT",
            body: JSON.stringify({
              mempoolUrl: services.mempoolApi,
              swapServiceUrl: services.swapServiceUrl,
              messageboardNwcUrl: services.messageboardNwcUrl,
              relay: services.relay,
              enableSwap: services.enableSwap,
              enableMessageboardNwc: services.enableMessageboardNwc,
              lsps: services.lsps.map(l => ({
                  name: l.name,
                  pubkey: l.pubkey,
                  host: l.host,
                  active: l.active,
              })),
            }),
          }),
        ]);
        
        toast.success("Configuration updated. Reloading...");
        setTimeout(() => window.location.reload(), 1000);
      } catch (error) {
          console.error(error);
          toast.error("Failed to update configuration");
      } finally {
          setLoading(false);
      }
  };

  return (
    <div className="flex items-center justify-center min-h-screen p-4 bg-background overflow-y-auto">
      <Card className="w-full max-w-md my-8">
        <CardHeader>
          <CardTitle className="text-destructive">Critical Error</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-muted-foreground">
            Something went wrong and Lokihub cannot continue.
          </p>
          <div className="p-3 bg-muted rounded-md font-mono text-sm break-all max-h-40 overflow-y-auto">
            {message}
          </div>

          <div className="pt-4">
            <Button
              variant="outline"
              onClick={() => setShowConfig(!showConfig)}
              className="w-full"
            >
              {showConfig ? "Hide Configuration" : "Update Configuration"}
            </Button>
          </div>

          {showConfig && (
            <div className="space-y-3 pt-2">
              {loadingConfig && <div className="text-sm text-muted-foreground">Loading configuration...</div>}
              {/* 1. Flokicoin Explorer */}
              {/* 1. Flokicoin Explorer */}
              <Card>
                <CardHeader>
                    <CardTitle className="text-base">Flokicoin Explorer</CardTitle>
                    <CardDescription>URL for Flokicoin transaction data.</CardDescription>
                </CardHeader>
                <CardContent>
                  <ServiceCardSelector
                    value={services.mempoolApi}
                    onChange={(val) => setServices({ ...services, mempoolApi: val })}
                    options={communityOptions.mempool}
                    placeholder="https://..."
                  />
                </CardContent>
              </Card>

              
              {/* 5. Nostr Relay */}
              {/* 5. Nostr Relay */}
              <Card>
                <CardHeader>
                    <CardTitle className="text-base">Nostr Relays</CardTitle>
                    <CardDescription>Multiple relays for Nostr Wallet Connect (NWC).</CardDescription>
                </CardHeader>
                <CardContent>
                  <MultiRelayInput
                    value={services.relay}
                    onChange={(newRelay) => setServices({ ...services, relay: newRelay })}
                    options={communityOptions.relay}
                    placeholder="wss://..."
                  />
                </CardContent>
              </Card>

              {/* 4. Swap Service URL */}
              {/* 4. Swap Service URL */}
              <Card>
                <CardHeader className="flex flex-row items-start justify-between space-y-0 pb-2">
                    <div className="space-y-1">
                        <CardTitle className="text-base">Swap Service URL</CardTitle>
                        <CardDescription>Service for atomic swaps between Lightning and on-chain.</CardDescription>
                    </div>
                    <div className="flex items-center space-x-2">
                      <Label htmlFor="enableSwap" className="text-sm font-normal">Enable</Label>
                      <Switch
                        id="enableSwap"
                        checked={services.enableSwap}
                        onCheckedChange={(checked) =>
                            setServices({ ...services, enableSwap: checked })
                        }
                      />
                    </div>
                </CardHeader>
                {services.enableSwap && (
                    <CardContent>
                    <ServiceCardSelector
                        value={services.swapServiceUrl}
                        onChange={(val) => setServices({ ...services, swapServiceUrl: val })}
                        options={communityOptions.swap}
                        placeholder="https://..."
                    />
                    </CardContent>
                )}
              </Card>


               {/* 6. Messageboard NWC URL */}
              {/* 6. Messageboard NWC URL */}
              <Card>
                <CardHeader className="flex flex-row items-start justify-between space-y-0 pb-2">
                    <div className="space-y-1">
                        <CardTitle className="text-base">Messageboard NWC URL</CardTitle>
                        <CardDescription>URL for the Lightning messageboard widget.</CardDescription>
                    </div>
                    <div className="flex items-center space-x-2">
                      <Label htmlFor="enableMessageboardNwc" className="text-sm font-normal">Enable</Label>
                      <Switch
                          id="enableMessageboardNwc"
                          checked={services.enableMessageboardNwc}
                          onCheckedChange={(checked) =>
                              setServices({ ...services, enableMessageboardNwc: checked })
                          }
                      />
                    </div>
                </CardHeader>
                {services.enableMessageboardNwc && (
                    <CardContent>
                        <ServiceCardSelector
                            value={services.messageboardNwcUrl}
                            onChange={(val) => setServices({ ...services, messageboardNwcUrl: val })}
                            options={communityOptions.messageboard}
                            placeholder="nostr+walletconnect://..."
                        />
                    </CardContent>
                )}
              </Card>

              {/* LSP Management */}
              <LSPManagementCard
                localLSPs={services.lsps}
                setLocalLSPs={(lsps) => setServices({ ...services, lsps })}
                className="border-border shadow-sm"
              />

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

              <Button onClick={saveConfiguration} disabled={loading} className="w-full">
                {loading ? <RotateCw className="w-4 h-4 mr-2 animate-spin" /> : null}
                {loading ? "Saving..." : "Save & Reload App"}
              </Button>
            </div>
          )}
        </CardContent>
        <CardFooter className="flex justify-end gap-2">
          {!showConfig && (
            <Button onClick={() => window.location.reload()}>
              <RotateCw className="w-4 h-4 mr-2" />
              Reload App
            </Button>
          )}
        </CardFooter>
      </Card>
    </div>
  );
}
