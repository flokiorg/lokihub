import {
  AlertCircle,
  Globe,
  PenLine,
  Server,
  Zap
} from "lucide-react";
import { nip47 } from "nostr-tools";
import React, { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { MultiRelayInput } from "src/components/MultiRelayInput";
import { ServiceCardSelector } from "src/components/ServiceCardSelector";
import { ServiceConfigurationHeader } from "src/components/ServiceConfigurationHeader";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Switch } from "src/components/ui/switch";
import useSetupStore from "src/state/SetupStore";
import { request } from "src/utils/request";
import { SetupLayout } from "./SetupLayout";

interface ServiceOption {
  name: string;
  value: string;
  description?: string;
  recommended?: boolean;
}

interface CommunityServices {
  rgs: ServiceOption[];
  swap: ServiceOption[];
  relay: ServiceOption[];
  messageboard: ServiceOption[];
  mempool: ServiceOption[];
}

export function SetupServices() {
  const navigate = useNavigate();
  const store = useSetupStore();
  
  const [loading, setLoading] = useState(true);
  
  const [swapServiceUrl, setSwapServiceUrl] = useState(store.nodeInfo.swapServiceUrl || "");
  const [relay, setRelay] = useState(store.nodeInfo.relay || "");
  const [messageboardNwcUrl, setMessageboardNwcUrl] = useState(store.nodeInfo.messageboardNwcUrl || "");
  const [mempoolApi, setMempoolApi] = useState(store.nodeInfo.mempoolApi || "");
  const [enableSwap, setEnableSwap] = useState(store.nodeInfo.enableSwap ?? false);
  const [enableMessageboardNwc, setEnableMessageboardNwc] = useState(store.nodeInfo.enableMessageboardNwc ?? false);

  const [validationErrors, setValidationErrors] = useState<string[]>([]);
  const errorRef = useRef<HTMLDivElement>(null);

  // Community options state
  const [communityOptions, setCommunityOptions] = useState<CommunityServices>({
    rgs: [], // Not used in this step but part of the structure
    swap: [],
    relay: [],
    messageboard: [],
    mempool: [],
  });

  // Fetch default values and community options
  useEffect(() => {
    // Scroll to top on mount
    window.scrollTo(0, 0);

    async function fetchServices() {
      try {
        setLoading(true);
        // GET /api/setup/config which returns the aggregator JSON
        const services = await request<any>("/api/setup/config", {
           method: "GET",
        });
        if (services) {
            setCommunityOptions({
                rgs: [],
                swap: services.swap_service || [],
                relay: services.nostr_relay || [],
                messageboard: services.messageboard_nwc || [],
                mempool: services.flokicoin_explorer || [],
            });
        }
        
        // Also fetch current info to prepopulate defaults if store is empty
        const info = await request<any>("/api/info", { method: "GET" });
        if (info) {
             // Populate defaults only if state is empty
            if (!swapServiceUrl && info.swapServiceUrl) {
              setSwapServiceUrl(info.swapServiceUrl);
            }
            if (!relay && info.relay) {
              setRelay(info.relay);
            }
            if (!messageboardNwcUrl && info.messageboardNwcUrl) {
                setMessageboardNwcUrl(info.messageboardNwcUrl);
            }
            if (!mempoolApi && info.mempoolUrl) {
                setMempoolApi(info.mempoolUrl);
            }
            // Populate boolean defaults only if they are not already set in the store
            if (store.nodeInfo.enableSwap === undefined && info.enableSwap !== undefined) {
                setEnableSwap(info.enableSwap);
            }
            if (store.nodeInfo.enableMessageboardNwc === undefined && info.enableMessageboardNwc !== undefined) {
                setEnableMessageboardNwc(info.enableMessageboardNwc);
            }
        }

      } catch (err) {
        console.error("Failed to fetch services or info", err);
        toast.error("Failed to fetch Hub Configuration. Please check your connection.");
      } finally {
        setLoading(false);
      }
    }
    fetchServices();
  }, []); // Run once on mount

  const validateUrl = (url: string, name: string, protocols: string[] = ["https"]): string | null => {
    if (!url) return `${name} is required.`;
    
    // Check if URL starts with one of the allowed protocols
    const isValidProtocol = protocols.some(protocol => url.startsWith(`${protocol}://`));
    
    if (!isValidProtocol) {
        return `${name} must start with ${protocols.map(p => p + "://").join(" or ")}`;
    }
    return null;
  };

  const validate = (): boolean => {
      const errors: string[] = [];
      
      // Assuming lokihubServicesURL is always required

      if (enableSwap) {
          const swapErr = validateUrl(swapServiceUrl, "Swap Service URL", ["https"]);
          if (swapErr) errors.push(swapErr);
      }

      if (enableMessageboardNwc) {
          if (!messageboardNwcUrl) {
              errors.push("Messageboard URL is required.");
          } else {
              try {
                  nip47.parseConnectionString(messageboardNwcUrl);
              } catch (e) {
                  errors.push("Invalid Messageboard NWC URL. It must be a valid nostr+walletconnect:// URI.");
              }
          }
      }
      
      
      // Validate relays (comma- separated)
      // Validate relays (comma- separated)
      if (!relay) {
          errors.push("At least one relay is required");
      } else {
        const relays = relay.split(",").map(r => r.trim()).filter(r => r.length > 0);
        if (relays.length === 0) {
            errors.push("At least one relay is required");
        }
        for (const relayUrl of relays) {
          const relayErr = validateUrl(relayUrl, "Nostr Relay URL", ["wss"]);
          if (relayErr) {
            errors.push(relayErr);
            break; // Only show first error
          }
        }
      }

      
      const mempoolErr = validateUrl(mempoolApi, "Flokicoin Explorer URL", ["https"]);
      if (mempoolErr) errors.push(mempoolErr);

      setValidationErrors(errors);
      return errors.length === 0;
  };


  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    
    if (!validate()) {
        // Scroll to error container
        setTimeout(() => {
            errorRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
        }, 100);
        return;
    }

    store.updateNodeInfo({
      swapServiceUrl,
      relay,
      messageboardNwcUrl,
      mempoolApi,
      enableSwap,
      enableMessageboardNwc,
    });
    navigate("/setup/node");
  }

  return (
    <SetupLayout
      backTo="/setup/password"
    >

      <TwoColumnLayoutHeader
          title="Hub Configuration"
          description="Configure external services for your node."
        />

      <form onSubmit={onSubmit} className="flex flex-col items-center w-full max-w-4xl mx-auto pb-10">

        <div className="w-full space-y-6 mt-6">
          <ServiceConfigurationHeader />
            
             {/* Flokicoin Explorer */}
            <Card className="border-border shadow-sm">
                <CardHeader className="pb-3">
                     <CardTitle className="text-lg flex items-center gap-2">
                        <Globe className="w-5 h-5 text-primary" />
                        Flokicoin Explorer
                    </CardTitle>
                    <CardDescription>
                         Used for fee estimation and transaction details.
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

             {/* Nostr Relays */}
            <Card className="border-border shadow-sm">
                <CardHeader className="pb-3">
                     <CardTitle className="text-lg flex items-center gap-2">
                        <Zap className="w-5 h-5 text-primary" />
                        Nostr Relays
                    </CardTitle>
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

             {/* Swap Service */}
             <Card className="border-border shadow-sm">
                 <CardHeader className="pb-3">
                     <div className="flex items-center justify-between">
                         <div className="space-y-1">
                             <CardTitle className="text-lg flex items-center gap-2">
                                <Server className="w-5 h-5 text-primary" />
                                 Swap Service
                             </CardTitle>
                             <CardDescription>
                                 Enables Lightning to on-chain swaps (and vice-versa).
                             </CardDescription>
                         </div>
                         <Switch checked={enableSwap} onCheckedChange={setEnableSwap} />
                     </div>
                 </CardHeader>
                 {enableSwap && (
                    <CardContent>
                         <ServiceCardSelector
                            value={swapServiceUrl}
                            onChange={setSwapServiceUrl}
                            options={communityOptions.swap}
                            placeholder="https://..."
                            disabled={!enableSwap}
                         />
                    </CardContent>
                 )}
            </Card>

             
            
             {/* Messageboard NWC */}
             <Card className="border-border shadow-sm">
                 <CardHeader className="pb-3">
                     <div className="flex items-center justify-between">
                         <div className="space-y-1">
                             <CardTitle className="text-lg flex items-center gap-2">
                                <PenLine className="w-5 h-5 text-primary" />
                                 Messageboard
                             </CardTitle>
                             <CardDescription>
                                 Connects to a NWC-enabled messageboard service.
                             </CardDescription>
                         </div>
                         <Switch checked={enableMessageboardNwc} onCheckedChange={setEnableMessageboardNwc} />
                     </div>
                 </CardHeader>
                 {enableMessageboardNwc && (
                    <CardContent>
                         <ServiceCardSelector
                            value={messageboardNwcUrl}
                            onChange={setMessageboardNwcUrl}
                            options={communityOptions.messageboard}
                            placeholder="nostr+walletconnect://..."
                            disabled={!enableMessageboardNwc}
                         />
                    </CardContent>
                 )}
            </Card>


            {validationErrors.length > 0 && (
              <div ref={errorRef} className="scroll-mt-4">
                  <Alert variant="destructive" className="mt-6 w-full animate-in fade-in slide-in-from-bottom-2">
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
        </div>

        

        <div className="flex justify-end w-full mt-8">
          <Button type="submit" size="lg" disabled={loading}>
            {loading ? "Loading..." : "Continue"}
          </Button>
        </div>
      </form>
    </SetupLayout>
  );
}
