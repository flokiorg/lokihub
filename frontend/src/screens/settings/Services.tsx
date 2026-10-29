import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useBlocker } from "react-router";
import { toast } from "sonner";
import Loading from "src/components/Loading";
import {
  ServiceConfigForm,
  ServiceConfigState,
  validateServiceConfig,
} from "src/components/ServiceConfigForm";
import { IdentityAuthorityManagementCard } from "src/components/settings/IdentityAuthorityManagementCard";
import { PrivateTransportStatusCard } from "src/components/settings/PrivateTransportStatusCard";
import SettingsHeader from "src/components/SettingsHeader";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "src/components/ui/alert-dialog";
import { Button } from "src/components/ui/button";
import { useIdentityAuthorities } from "src/hooks/useIdentityAuthorities";
import { useInfo } from "src/hooks/useInfo";
import { IdentityAuthority } from "src/types";
import { request } from "src/utils/request";

export function Services() {
  const { data: info, mutate: reloadInfo } = useInfo();
  const { t } = useTranslation("setup");

  const [config, setConfig] = useState<ServiceConfigState>({
    mempoolApi: "",
    relay: "",
    trustedNwcRelay: true,
    generalRelay: "",
    searchRelay: "",
    swapServiceUrl: "",
    messageboardNwcUrl: "",
    enableSwap: true,
    enableMessageboardNwc: true,
  });

  // Identity Authority Management Hook
  const {
    authorities: backendAuthorities,
    fetchIdentityAuthorities,
    saveIdentityAuthorityChanges,
    initialized: authoritiesInitialized,
  } = useIdentityAuthorities();
  const [localAuthorities, setLocalAuthorities] = useState<IdentityAuthority[]>(
    []
  );

  // Track changes for Save button
  const [servicesDirty, setServicesDirty] = useState(false);
  const [savingServices, setSavingServices] = useState(false);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);

  useEffect(() => {
    if (info) {
      setConfig((prev) => ({
        ...prev,
        swapServiceUrl: info.swapServiceUrl || "",
        messageboardNwcUrl: info.messageboardNwcUrl || "",
        relay: info.relay || "",
        trustedNwcRelay: info.trustedNwcRelay ?? true,
        generalRelay: info.generalRelay || "",
        searchRelay: info.searchRelay || "",
        mempoolApi: info.mempoolUrl || "",
        enableSwap: info.enableSwap ?? true,
        enableMessageboardNwc: info.enableMessageboardNwc ?? true,
      }));
    }
  }, [info]);

  useEffect(() => {
    fetchIdentityAuthorities();
  }, [fetchIdentityAuthorities]);

  // Sync local Identity Authorities with the backend once, then while clean.
  const hasSyncedAuthorities = useRef(false);
  useEffect(() => {
    if (authoritiesInitialized) {
      if (!hasSyncedAuthorities.current) {
        setLocalAuthorities(backendAuthorities);
        hasSyncedAuthorities.current = true;
      } else if (!servicesDirty) {
        setLocalAuthorities(backendAuthorities);
      }
    }
  }, [backendAuthorities, servicesDirty, authoritiesInitialized]);

  // Track dirty state
  useEffect(() => {
    if (!info) {
      return;
    }

    // Check general settings dirty
    const settingsDirty =
      config.relay !== (info.relay || "") ||
      config.trustedNwcRelay !== (info.trustedNwcRelay ?? true) ||
      config.generalRelay !== (info.generalRelay || "") ||
      config.searchRelay !== (info.searchRelay || "") ||
      config.mempoolApi !== (info.mempoolUrl || "") ||
      config.swapServiceUrl !== (info.swapServiceUrl || "") ||
      config.messageboardNwcUrl !== (info.messageboardNwcUrl || "") ||
      config.enableSwap !== (info.enableSwap ?? true) ||
      config.enableMessageboardNwc !== (info.enableMessageboardNwc ?? true);

    // Check Identity Authorities dirty - compare by pubkey/name/relay_urls, sorted.
    const stripAuthorities = (authorities: IdentityAuthority[]) =>
      authorities
        .map((a) => ({
          pubkey: a.pubkey,
          name: a.name,
          relay_urls: a.relay_urls ?? [],
        }))
        .sort((a, b) => a.pubkey.localeCompare(b.pubkey));

    const authoritiesDirtySmart =
      JSON.stringify(stripAuthorities(localAuthorities)) !==
      JSON.stringify(stripAuthorities(backendAuthorities));

    const hasChanges = settingsDirty || authoritiesDirtySmart;

    setServicesDirty(hasChanges);
    if (!hasChanges) {
      setValidationErrors([]);
    }
  }, [info, config, localAuthorities, backendAuthorities]);

  async function updateSettings(payload: Record<string, any>) {
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

  async function saveServices(): Promise<boolean> {
    setSavingServices(true);
    setValidationErrors([]);

    const errors = validateServiceConfig(config, t);

    if (errors.length > 0) {
      setValidationErrors(errors);
      setSavingServices(false);
      setTimeout(() => {
        document
          .getElementById("service-config-errors")
          ?.scrollIntoView({ behavior: "smooth", block: "center" });
      }, 100);
      return false;
    }

    try {
      // Trusted LSPs are saved at once from the catalogue, not here.
      // 1. Save Identity Authorities via the same diff-based approach
      await saveIdentityAuthorityChanges(backendAuthorities, localAuthorities);

      // 2. Save other settings via UpdateSettings
      await updateSettings({
        relay: config.relay,
        trustedNwcRelay: config.trustedNwcRelay,
        generalRelay: config.generalRelay,
        searchRelay: config.searchRelay,
        mempoolApi: config.mempoolApi,
        swapServiceUrl: config.swapServiceUrl,
        messageboardNwcUrl: config.messageboardNwcUrl,
        enableSwap: config.enableSwap,
        enableMessageboardNwc: config.enableMessageboardNwc,
        // Identity Authorities omitted, handled above
      });

      // Reload
      await reloadInfo();
      toast.success("Services updated successfully");

      setServicesDirty(false);
      return true;
    } catch (e: any) {
      toast.error("Failed to save services", { description: e.message });
      return false;
    } finally {
      setSavingServices(false);
    }
  }

  // Block in-app navigation away from this page while there are unsaved changes.
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      servicesDirty && currentLocation.pathname !== nextLocation.pathname
  );

  // Warn on tab close / refresh while there are unsaved changes.
  useEffect(() => {
    if (!servicesDirty) {
      return;
    }
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener("beforeunload", handleBeforeUnload);
    return () => window.removeEventListener("beforeunload", handleBeforeUnload);
  }, [servicesDirty]);

  async function handleSaveAndLeave() {
    const saved = await saveServices();
    if (saved && blocker.state === "blocked") {
      blocker.proceed();
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

          <PrivateTransportStatusCard
            privateTransport={info.privateTransport}
            className="border-border shadow-sm"
          />

          <IdentityAuthorityManagementCard
            localAuthorities={localAuthorities}
            setLocalAuthorities={setLocalAuthorities}
            className="border-border shadow-sm"
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

      <AlertDialog
        open={blocker.state === "blocked"}
        onOpenChange={(open) => {
          if (!open && blocker.state === "blocked") {
            blocker.reset();
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("services.unsavedChanges.title")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("services.unsavedChanges.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel
              onClick={() => blocker.state === "blocked" && blocker.reset()}
            >
              {t("services.unsavedChanges.stay")}
            </AlertDialogCancel>
            <Button
              type="button"
              variant="destructive"
              onClick={() => blocker.state === "blocked" && blocker.proceed()}
            >
              {t("services.unsavedChanges.discard")}
            </Button>
            <AlertDialogAction
              disabled={savingServices}
              onClick={handleSaveAndLeave}
            >
              {savingServices
                ? t("services.unsavedChanges.saving")
                : t("services.unsavedChanges.save")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
