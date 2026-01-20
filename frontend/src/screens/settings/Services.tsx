import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import Loading from "src/components/Loading";
import {
  ServiceConfigForm,
  ServiceConfigState,
  validateServiceConfig
} from "src/components/ServiceConfigForm";
import SettingsHeader from "src/components/SettingsHeader";
import { Button } from "src/components/ui/button";
import { useInfo } from "src/hooks/useInfo";
import { useLSPSManagement } from "src/hooks/useLSPSManagement";
import { LSP } from "src/types";
import { request } from "src/utils/request";


export function Services() {
  const { data: info, mutate: reloadInfo } = useInfo();
  
  const [config, setConfig] = useState<ServiceConfigState>({
      mempoolApi: "",
      relay: "",
      swapServiceUrl: "",
      messageboardNwcUrl: "",
      enableSwap: true,
      enableMessageboardNwc: true,
      lsps: [],
  });

  // LSP Management Hook
  const { lsps: backendLSPs, fetchLSPs, saveLSPChanges, initialized: lspInitialized } = useLSPSManagement();
  
  // Track changes for Save button
  const [servicesDirty, setServicesDirty] = useState(false);
  const [savingServices, setSavingServices] = useState(false);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);



  useEffect(() => {
    if (info) {
      setConfig(prev => ({
        ...prev,
        swapServiceUrl: info.swapServiceUrl || "",
        messageboardNwcUrl: info.messageboardNwcUrl || "",
        relay: info.relay || "",
        mempoolApi: info.mempoolUrl || "",
        enableSwap: info.enableSwap ?? true,
        enableMessageboardNwc: info.enableMessageboardNwc ?? true,
        // LSPs are handled separately via backendLSPs + community merge below
      }));
    }
  }, [info]);

  useEffect(() => {
    fetchLSPs();
  }, [fetchLSPs]);

  // Sync local LSPs with backend LSPs whenever backend updates and we aren't dirty
  // Also merge with community LSPs
  // Sync local LSPs with backend LSPs whenever backend updates and we aren't dirty
  const hasSyncedLSPs = useRef(false);

  // Sync local LSPs with backend LSPs whenever backend updates and we aren't dirty
  // We use hasSyncedLSPs to force the initial sync once backend data is ready
  useEffect(() => {
    if (lspInitialized) {
        if (!hasSyncedLSPs.current) {
             // First load: force sync
             setConfig(prev => ({ ...prev, lsps: backendLSPs }));
             hasSyncedLSPs.current = true;
        } else if (!servicesDirty) {
             // Subsequent updates: sync if clean
             setConfig(prev => ({ ...prev, lsps: backendLSPs }));
        }
    }
  }, [backendLSPs, servicesDirty, lspInitialized]);


  // Track dirty state
  useEffect(() => {
    if (!info) {
        return;
    }
    
    // Check general settings dirty
    const settingsDirty =
      config.relay !== (info.relay || "") ||
      config.mempoolApi !== (info.mempoolUrl || "") ||
      config.swapServiceUrl !== (info.swapServiceUrl || "") ||
      config.messageboardNwcUrl !== (info.messageboardNwcUrl || "") ||
      config.enableSwap !== (info.enableSwap ?? true) ||
      config.enableMessageboardNwc !== (info.enableMessageboardNwc ?? true);

    // Check LSP dirty - simple JSON comparison for now
    // Note: this assumes ordering maps correctly. We should probably sort or use a deeper comparison if order matters.
    // Ideally compare contents.

    // Wait, backendLSPs might not have "isCommunity" flags or descriptions fully populated if they came from backend only,
    // but the local config.lsps has them.
    // Comparison is tricky.
    // Let's assume if the user didn't change anything, the merged result is what we have.
    // But `backendLSPs` is the source of truth for "saved" LSPs.
    // Actually, `mergeLSPs` modifies the objects.
    // So comparing `config.lsps` (merged) with `backendLSPs` (raw) will ALWAYS be different if descriptions are added.
    // We should compare the "Saveable" parts: pubkey, host, name, active.
    
    // Helper to strip extra fields
    const strip = (lsps: LSP[]) => lsps
        .map(l => ({ 
            pubkey: l.pubkey, host: l.host, name: l.name, active: l.active 
        })).sort((a,b) => a.pubkey.localeCompare(b.pubkey));
    
    const lspDirtySmart = JSON.stringify(strip(config.lsps)) !== JSON.stringify(strip(backendLSPs));

    const hasChanges = settingsDirty || lspDirtySmart;

    setServicesDirty(hasChanges);
    if (!hasChanges) {
      setValidationErrors([]);
    }
  }, [info, config, backendLSPs]);


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
    } catch (error) {
      console.error(error);
      throw error; // Let caller handle
    }
  }

  async function saveServices() {
    setSavingServices(true);
    setValidationErrors([]);
    
    const errors = validateServiceConfig(config);

    if (errors.length > 0) {
        setValidationErrors(errors);
        setSavingServices(false);
        setTimeout(() => {
            document.getElementById("service-config-errors")?.scrollIntoView({ behavior: "smooth", block: "center" });
        }, 100);
        return;
    }

    try {
      // 1. Save LSPs via atomic hook
      // We pass the ORIGINAL backendLSPs and the NEW config.lsps
      // The hook calculates diffs and issues Add/Delete/Toggle requests
      if (config.lsps && backendLSPs) {
          await saveLSPChanges(backendLSPs, config.lsps);
      }

      // 2. Save other settings via UpdateSettings
      await updateSettings({
        relay: config.relay,
        mempoolApi: config.mempoolApi,
        swapServiceUrl: config.swapServiceUrl,
        messageboardNwcUrl: config.messageboardNwcUrl,
        enableSwap: config.enableSwap,
        enableMessageboardNwc: config.enableMessageboardNwc,
        // LSPs omitted, handled above
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
      <div className="w-full flex flex-col gap-8">
        {/* Services Section */}
        <div className="space-y-4">
          
          <ServiceConfigForm 
            state={config} 
            onChange={setConfig} 
            validationErrors={validationErrors}
          />

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
    </>
  );
}
