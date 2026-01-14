import { AlertCircle, ArrowRightLeft, Check, Droplet, MessageCircle, Plus, Server, Trash2 } from "lucide-react";
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
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Switch } from "src/components/ui/switch";
import { useInfo } from "src/hooks/useInfo";
import { LSP, useLSPSManagement } from "src/hooks/useLSPSManagement";
import { cn } from "src/lib/utils";
import { request } from "src/utils/request";
import { validateHTTPURL, validateLSPURI, validateMessageBoardURL, validateWebSocketURL } from "src/utils/validation";

export function centerTrim(text: string, keepStart = 8, keepEnd = 8) {
  if (!text || text.length <= keepStart + keepEnd) return text;
  return `${text.slice(0, keepStart)}...${text.slice(-keepEnd)}`;
}
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
  const [newLSPName, setNewLSPName] = useState("");
  const [newLSPURI, setNewLSPURI] = useState("");
  const [isAddingLSP, setIsAddingLSP] = useState(false);

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


  const separateLSPs = () => {
    const community = localLSPs.filter(l => l.isCommunity);
    const custom = localLSPs.filter(l => !l.isCommunity);
    return { community, custom };
  };

  const { community: communityCards, custom: customCards } = separateLSPs();


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


  const handleAddLocalLSP = () => {
    setValidationErrors([]);
    const errors: string[] = [];
    
    if (!newLSPName.trim()) {
        errors.push("LSP Name is required");
    } else if (localLSPs.some(l => l.name.toLowerCase() === newLSPName.toLowerCase())) {
        errors.push("LSP Name must be unique");
    }

    if (!newLSPURI.trim()) {
        errors.push("LSP URI is required");
    } else {
        const uriErr = validateLSPURI(newLSPURI);
        if (uriErr) errors.push(uriErr);
    }

    if (errors.length > 0) {
        setValidationErrors(errors);
        return;
    }

    // Parse URI to get pubkey and host
    const parts = newLSPURI.split('@');
    if (parts.length !== 2) {
        // Should be caught by validation
        return;
    }
    const pubkey = parts[0];
    const host = parts[1];
    
    // Check if pubkey exists already locally
    if (localLSPs.some(l => l.pubkey === pubkey)) {
        setValidationErrors(["LSP with this Pubkey already exists"]);
        return;
    }

    const newLSP: LSP = {
        name: newLSPName,
        pubkey: pubkey,
        host: host,
        active: true, // Default to active? Or inactive? Let's say true for convenience.
    };

    setLocalLSPs([...localLSPs, newLSP]);
    setNewLSPName("");
    setNewLSPURI("");
    setIsAddingLSP(false);
  };

  const removeLocalLSP = (pubkey: string) => {
      setLocalLSPs(localLSPs.filter(l => l.pubkey !== pubkey));
  };
  
  const toggleLocalLSP = (pubkey: string, active: boolean) => {
      setLocalLSPs(localLSPs.map(l => l.pubkey === pubkey ? {...l, active} : l));
  };


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
          <Card>
              <CardHeader>
                  <CardTitle className="text-base">Lightning Service Providers</CardTitle>
                  <CardDescription>
                      Manage LSPs for JIT channels and inbound liquidity.
                  </CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                  <div className="grid grid-cols-[repeat(auto-fit,minmax(320px,1fr))] gap-4">
                      {/* Community LSPs Section */}
                                            
                      {/* Community Services (Implicit top section) */}
                      
                      {communityCards.map((provider) => (
                          <div 
                              key={provider.pubkey}
                              onClick={() => toggleLocalLSP(provider.pubkey, !provider.active)}
                              className={cn(
                                "relative group flex flex-col p-4 rounded-xl border transition-all duration-200 cursor-pointer select-none",
                                "hover:shadow-md active:scale-[0.98]",
                                provider.active 
                                  ? "border-primary bg-primary/5 shadow-sm" 
                                  : "border-border bg-card hover:border-primary/50"
                              )}
                          >
                                  <div className="flex items-start justify-between">
                                      <div className="flex items-center gap-2">
                                          <div className={cn(
                                              "p-2 rounded-lg transition-colors",
                                              provider.active ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"
                                          )}>
                                              <Droplet className="w-4 h-4"/>
                                          </div>
                                          <div className="flex flex-col">
                                              <span className="font-semibold text-sm leading-none">{provider.name}</span>
                                              <span className="text-[10px] text-muted-foreground font-medium mt-1">
                                                  {provider.active ? "Active" : "Inactive"}
                                              </span>
                                          </div>
                                      </div>
                                      {provider.active && (
                                          <div className="bg-primary text-primary-foreground rounded-full p-0.5 shrink-0">
                                              <Check className="w-3 h-3" />
                                          </div>
                                      )}
                                  </div>
                                  
                                  {provider.description && (
                                     <p className="text-xs text-muted-foreground line-clamp-2 leading-snug mt-3">{provider.description}</p>
                                  )}
                                  
                                  <div className="flex flex-col gap-1.5 mt-3 pt-3 border-t border-border/50">
                                      <div className="flex items-center justify-between gap-2">
                                          <p className="text-[10px] text-muted-foreground opacity-50 font-mono truncate cursor-help" title={`${provider.pubkey}@${provider.host}`}>
                                              {centerTrim(provider.pubkey)}@{provider.host}
                                          </p>
                                      </div>
                                  </div>
                          </div>
                      ))}

                      {/* Custom LSPs Section Separator */}
                      <div className="col-span-full flex items-center gap-4 py-2">
                          <div className="h-px bg-border flex-1" />
                          <span className="text-xs text-muted-foreground font-medium uppercase tracking-wider opacity-50">My Services</span>
                          <div className="h-px bg-border flex-1" />
                      </div>

                      {customCards.map((provider) => (
                          <div 
                              key={provider.pubkey}
                              onClick={(e) => {
                                  // Don't toggle if clicking delete
                                  if ((e.target as HTMLElement).closest('button')) return;
                                  toggleLocalLSP(provider.pubkey, !provider.active);
                              }}
                              className={cn(
                                "relative group flex flex-col p-4 rounded-xl border transition-all duration-200 cursor-pointer select-none",
                                "hover:shadow-md active:scale-[0.98]",
                                provider.active 
                                  ? "border-primary bg-primary/5 shadow-sm" 
                                  : "border-border bg-card hover:border-primary/50"
                              )}
                          >
                                  <div className="flex items-start justify-between">
                                      <div className="flex items-center gap-2">
                                          <div className={cn(
                                              "p-2 rounded-lg transition-colors",
                                              provider.active ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"
                                          )}>
                                              <Droplet className="w-4 h-4"/>
                                          </div>
                                          <div className="flex flex-col">
                                              <span className="font-semibold text-sm leading-none">{provider.name}</span>
                                              <span className="text-[10px] text-muted-foreground font-medium mt-1">
                                                  {provider.active ? "Active" : "Inactive"}
                                              </span>
                                          </div>
                                      </div>
                                      <div className="flex items-center gap-2">
                                        <Button
                                            variant="ghost"
                                            size="icon"
                                            className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                                            onClick={(e) => {
                                                e.stopPropagation();
                                                removeLocalLSP(provider.pubkey);
                                            }}
                                        >
                                            <Trash2 className="w-3 h-3" />
                                        </Button>
                                        {provider.active && (
                                            <div className="bg-primary text-primary-foreground rounded-full p-0.5 shrink-0">
                                                <Check className="w-3 h-3" />
                                            </div>
                                        )}
                                      </div>
                                  </div>
                                  
                                  <div className="flex flex-col gap-1.5 mt-3 pt-3 border-t border-border/50">
                                      <div className="flex items-center justify-between gap-2">
                                          <p className="text-[10px] text-muted-foreground opacity-50 font-mono truncate cursor-help" title={`${provider.pubkey}@${provider.host}`}>
                                              {centerTrim(provider.pubkey)}@{provider.host}
                                          </p>
                                      </div>
                                  </div>
                          </div>
                      ))}

                      {/* Add New LSP Card */}
                      <div 
                          className={cn(
                              "relative flex flex-col p-4 rounded-xl border border-dashed border-border transition-all duration-200",
                              isAddingLSP 
                                ? "bg-card shadow-md ring-1 ring-primary border-primary" 
                                : "bg-transparent hover:border-primary hover:shadow-sm cursor-pointer group"
                          )}
                          onClick={() => !isAddingLSP && setIsAddingLSP(true)}
                      >
                          {!isAddingLSP ? (
                              <div className="flex flex-col items-center justify-center h-full py-6 text-muted-foreground group-hover:text-primary transition-colors">
                                  <div className="p-3 rounded-full bg-muted/50 mb-3 group-hover:bg-primary/10 group-hover:scale-110 transition-all duration-300">
                                      <Plus className="w-5 h-5"/>
                                  </div>
                                  <span className="font-medium text-sm">Add Custom LSP</span>
                              </div>
                          ) : (
                              <div className="flex flex-col h-full animate-in fade-in zoom-in-95 duration-200">
                                  <div className="flex items-center justify-between mb-4">
                                      <span className="font-semibold text-sm">New Service</span>
                                      <Server className="w-4 h-4 text-muted-foreground" />
                                  </div>
                                  
                                  <div className="flex-1 space-y-3">
                                      <div className="space-y-1">
                                          <Label htmlFor="lsp-name" className="text-xs">Name</Label>
                                          <Input
                                              id="lsp-name"
                                              value={newLSPName}
                                              onChange={(e) => setNewLSPName(e.target.value)}
                                              placeholder="My Node"
                                              className="h-8 text-xs bg-background"
                                              autoFocus
                                              onKeyDown={(e) => {
                                                  if (e.key === 'Enter') handleAddLocalLSP();
                                                  if (e.key === 'Escape') {
                                                      setIsAddingLSP(false);
                                                      setNewLSPName("");
                                                      setNewLSPURI("");
                                                  }
                                              }}
                                          />
                                      </div>
                                      <div className="space-y-1">
                                          <Label htmlFor="lsp-uri" className="text-xs">URI (pubkey@host:port)</Label>
                                          <Input
                                              id="lsp-uri"
                                              value={newLSPURI}
                                              onChange={(e) => {
                                                  setNewLSPURI(e.target.value);
                                              }}
                                              placeholder="02abc...@127.0.0.1:9735"
                                              className={cn(
                                                  "h-8 text-xs bg-background font-mono",
                                                  newLSPURI && validateLSPURI(newLSPURI) && "border-destructive focus-visible:ring-destructive"
                                              )}
                                              onKeyDown={(e) => {
                                                  if (e.key === 'Enter') handleAddLocalLSP();
                                                  if (e.key === 'Escape') {
                                                      setIsAddingLSP(false);
                                                      setNewLSPName("");
                                                      setNewLSPURI("");
                                                  }
                                              }}
                                          />
                                          {newLSPURI && validateLSPURI(newLSPURI) && (
                                              <p className="text-[10px] text-destructive font-medium mt-1">
                                                  {validateLSPURI(newLSPURI)}
                                              </p>
                                          )}
                                      </div>
                                  </div>

                                  <div className="flex items-center gap-2 mt-4 pt-2">
                                      <Button 
                                          size="sm" 
                                          variant="outline" 
                                          className="flex-1 h-7 text-xs"
                                          onClick={(e) => {
                                              e.stopPropagation();
                                              setIsAddingLSP(false);
                                              setNewLSPName("");
                                              setNewLSPURI("");
                                          }}
                                      >
                                          Cancel
                                      </Button>
                                      <Button 
                                          size="sm" 
                                          className="flex-1 h-7 text-xs"
                                          onClick={(e) => {
                                              e.stopPropagation();
                                              handleAddLocalLSP();
                                          }}
                                          disabled={!newLSPName || !newLSPURI || !!validateLSPURI(newLSPURI)}
                                      >
                                          Add
                                      </Button>
                                  </div>
                              </div>
                          )}
                      </div>
                  </div>
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
