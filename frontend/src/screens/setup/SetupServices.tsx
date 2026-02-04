import React, { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
    ServiceConfigForm,
    ServiceConfigState,
    validateServiceConfig
} from "src/components/ServiceConfigForm";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { Button } from "src/components/ui/button";
import useSetupStore from "src/state/SetupStore";
import { LSP } from "src/types";
import { request } from "src/utils/request";
import { SetupLayout } from "./SetupLayout";

export function SetupServices() {
  const navigate = useNavigate();
  const store = useSetupStore();
  
  const [loading, setLoading] = useState(true);
  
  const [config, setConfig] = useState<ServiceConfigState>({
      mempoolApi: store.nodeInfo.mempoolApi || "",
      relay: store.nodeInfo.relay || "",
      swapServiceUrl: store.nodeInfo.swapServiceUrl || "",
      messageboardNwcUrl: store.nodeInfo.messageboardNwcUrl || "",
      enableSwap: store.nodeInfo.enableSwap ?? false,
      enableMessageboardNwc: store.nodeInfo.enableMessageboardNwc ?? false,
      lsps: store.nodeInfo.lsps || [],
  });

  const [validationErrors, setValidationErrors] = useState<string[]>([]);


  // Fetch default values and community options
  useEffect(() => {
    // Scroll to top on mount
    window.scrollTo(0, 0);

    async function fetchServices() {
      try {
        setLoading(true);
        // Get community services config to merge LSPs
        const services = await request<any>("/api/setup/config", {
           method: "GET",
        });
        
        const communityLSPs = services?.lsps || [];
        
        // Also fetch current info to prepopulate defaults if store is empty
        const info = await request<any>("/api/info", { method: "GET" });
        if (info) {
             // We need to construct the new state based on info + store
             
             // Helper to pick value: store > info > default
             // Actually, store is initialized from empty/previous steps.
             // If store values are empty, use info.
             
             setConfig(prev => {
                 const newConfig = { ...prev };
                 if (!newConfig.swapServiceUrl && info.swapServiceUrl) newConfig.swapServiceUrl = info.swapServiceUrl;
                 if (!newConfig.relay && info.relay) newConfig.relay = info.relay;
                 if (!newConfig.messageboardNwcUrl && info.messageboardNwcUrl) newConfig.messageboardNwcUrl = info.messageboardNwcUrl;
                 if (!newConfig.mempoolApi && info.mempoolUrl) newConfig.mempoolApi = info.mempoolUrl;
                 
                 // Boolean defaults
                 if (store.nodeInfo.enableSwap === undefined && info.enableSwap !== undefined) newConfig.enableSwap = info.enableSwap;
                 if (store.nodeInfo.enableMessageboardNwc === undefined && info.enableMessageboardNwc !== undefined) newConfig.enableMessageboardNwc = info.enableMessageboardNwc;
                 
                 // LSPs
                 if (newConfig.lsps.length === 0) {
                     const existingLSPs = (info.lsps as LSP[]) || [];
                     if (existingLSPs.length > 0) {
                        newConfig.lsps = existingLSPs;
                     } else {
                        // Use community LSPs as defaults
                        newConfig.lsps = communityLSPs.map((opt: any) => {
                            const connection = opt.connection || opt.uri || "";
                            const [pubkeyRaw, host] = connection.split('@');
                            return {
                                name: opt.name,
                                pubkey: pubkeyRaw,
                                host: host,
                                active: false, // Default to inactive until user selects
                                isCommunity: true,
                                description: opt.description,
                                website: opt.url || opt.website
                            } as LSP;
                        });
                     }
                 }
                 
                 return newConfig;
             });
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


  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    
    const errors = validateServiceConfig(config);
    if (errors.length > 0) {
        setValidationErrors(errors);
        // Scroll to error container
        setTimeout(() => {
            document.getElementById("service-config-errors")?.scrollIntoView({ behavior: "smooth", block: "center" });
        }, 100);
        return;
    }

    store.updateNodeInfo({
      swapServiceUrl: config.swapServiceUrl,
      relay: config.relay,
      messageboardNwcUrl: config.messageboardNwcUrl,
      mempoolApi: config.mempoolApi,
      enableSwap: config.enableSwap,
      enableMessageboardNwc: config.enableMessageboardNwc,
      lsps: config.lsps,
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
            <ServiceConfigForm 
                state={config} 
                onChange={setConfig} 
                validationErrors={validationErrors}
            />
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
