import { AlertCircle, ArrowRightLeft, MessageCircle } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import Loading from "src/components/Loading";
import { LSPManagementCard } from "src/components/LSPManagementCard";
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
import { LSP, useLSPSManagement } from "src/hooks/useLSPSManagement";
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
    lsp: ServiceOption[];
  }>({
    swap: [],
    relay: [],
    messageboard: [],
    mempool: [],
    lsp: [],
  });

  const errorRef = useRef<HTMLDivElement>(null);

  const [swapServiceUrl, setSwapServiceUrl] = useState("");
  const [messageboardNwcUrl, setMessageboardNwcUrl] = useState("");
  const [relay, setRelay] = useState("");
  const [mempoolApi, setMempoolApi] = useState("");
  const [enableSwap, setEnableSwap] = useState(true);
  const [enableMessageboardNwc, setEnableMessageboardNwc] = useState(true);

  // LSP Management Hook
  const { lsps: backendLSPs, fetchLSPs } = useLSPSManagement();
  
  // Local LSP State for Batch Saving
  const [localLSPs, setLocalLSPs] = useState<LSP[]>([]);

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
    fetchLSPs();
  }, [fetchLSPs]);

  // Sync local LSPs with backend LSPs whenever backend updates and we aren't dirty
  // Also merge with community LSPs
  useEffect(() => {
    if (backendLSPs && !servicesDirty) {
        // Merge community LSPs with backend LSPs
        const communityLSPs = (communityOptions.lsp || []).map(opt => {
            // Parse URI to get pubkey and host
            const [pubkey, host] = opt.uri?.split('@') || ['', ''];
            // Check if this community LSP is in the backend list
            // Check if this community LSP is in the backend list
            const existingLSP = backendLSPs.find(lsp => lsp.pubkey === pubkey);
            
            if (existingLSP) {
                // If it exists in backend, we must enrich it with community data
                return {
                    ...existingLSP,
                    isCommunity: true,
                    description: opt.description
                };
            }
            
            return {
                name: opt.name,
                pubkey: pubkey,
                host: host,
                active: false,
                isCommunity: true,
                description: opt.description
            };
        });
        
        // Get custom LSPs (not in community list)
        const communityPubkeys = new Set((communityOptions.lsp || []).map(opt => opt.uri?.split('@')[0]));
        const customLSPs = backendLSPs.filter(lsp => !communityPubkeys.has(lsp.pubkey));
        
        // Merge: community LSPs first, then custom LSPs
        setLocalLSPs([...communityLSPs, ...customLSPs]);
    }
  }, [backendLSPs, servicesDirty, communityOptions.lsp]);





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
                    lsp: services.lsps || [], 
                });
            }
        } catch (error) {
            console.error(error);
            toast.error("Failed to fetch community services");
        }
    }
    fetchServices();
  }, []);

  // Track dirty state
  useEffect(() => {
    if (!info) return;
    
    // Check general settings dirty
    const settingsDirty =
      relay !== (info.relay || "") ||
      mempoolApi !== (info.mempoolUrl || "") ||
      swapServiceUrl !== (info.swapServiceUrl || "") ||
      messageboardNwcUrl !== (info.messageboardNwcUrl || "") ||
      enableSwap !== (info.enableSwap ?? true) ||
      enableMessageboardNwc !== (info.enableMessageboardNwc ?? true);

    // Check LSP dirty - simple JSON comparison for now
    // Note: this assumes ordering maps correctly. We should probably sort or use a deeper comparison if order matters.
    // Ideally compare contents.
    const lspDirty = JSON.stringify(localLSPs) !== JSON.stringify(backendLSPs);

    const hasChanges = settingsDirty || lspDirty;

    setServicesDirty(hasChanges);
    if (!hasChanges) {
      setValidationErrors([]);
    }
  }, [info, relay, mempoolApi, swapServiceUrl, messageboardNwcUrl, enableSwap, enableMessageboardNwc, localLSPs, backendLSPs]);





  async function updateSettings(
    payload: Record<string, any>
  ) {
    try {
      await request("/api/settings", {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(payload),
      });
      // Don't toast here, wait for full chain in saveServices
      // await reloadInfo();
    } catch (error) {
      console.error(error);
      throw error; // Let caller handle
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
        setTimeout(() => {
          errorRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
        }, 100);
        return;
      }

      // Save all settings including LSPs in one request
      await updateSettings({
        relay,
        mempoolApi,
        swapServiceUrl,
        messageboardNwcUrl,
        enableSwap,
        enableMessageboardNwc,
        lsps: localLSPs.map(lsp => ({
          name: lsp.name,
          pubkey: lsp.pubkey,
          host: lsp.host,
          active: lsp.active,
        })),
      });

      // Reload
      await Promise.all([
          reloadInfo(),
          fetchLSPs()
      ]);
      toast.success("Services updated successfully");
      
      setServicesDirty(false);
    } catch (e: any) {
        toast.error("Failed to save services", { description: e.message });
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
          
          {/* LSP Management */}
          <LSPManagementCard
            localLSPs={localLSPs}
            setLocalLSPs={setLocalLSPs}
          />

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
                          customLabel="Swap Service URL"
                          customIcon={<ArrowRightLeft className="w-5 h-5"/>}
                          fullWidth
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
                          customLabel="Messageboard NWC URL"
                          customIcon={<MessageCircle className="w-5 h-5"/>}
                          fullWidth
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
