import { AlertCircle, Globe, LayoutGrid, Search, Users, Zap } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { LSPCatalogue } from "src/components/lsps/LSPCatalogue";
import { MultiRelayInput } from "src/components/MultiRelayInput";
import {
  ServiceCardSelector,
  ServiceOption,
} from "src/components/ServiceCardSelector";
import { ServiceConfigurationHeader } from "src/components/ServiceConfigurationHeader";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Switch } from "src/components/ui/switch";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "src/components/ui/tabs";
import { request } from "src/utils/request";
import { validateHTTPURL, validateWebSocketURL } from "src/utils/validation";

export interface ServiceConfigState {
  mempoolApi: string;
  relay: string;
  trustedNwcRelay: boolean;
  generalRelay: string;
  searchRelay: string;
  // App-store base URL. Empty is a valid state: the app store is OFF (no sync,
  // no fetch) until a store is selected here.
  lokihubStoreUrl: string;
}

export interface ServiceConfigFormProps {
  state: ServiceConfigState;
  onChange: (state: ServiceConfigState) => void;
  className?: string;
  validationErrors?: string[];
  // General/Search relay configuration isn't part of initial setup/recovery
  // flows (those save via SetupRequest/SetupLocalRequest/SetupManualRequest,
  // which have no generalRelay/searchRelay field) — only the main Settings >
  // Services screen (PATCH /api/settings) persists them. Default to true.
  showGeneralRelay?: boolean;
  showSearchRelay?: boolean;
  // The app store is a main-Settings concern (PATCH /api/settings persists it);
  // the initial setup flow doesn't save it, so it's hidden there.
  showAppStore?: boolean;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type TFunction = (key: any, options?: any) => string;

const fallbackT: TFunction = (
  key: string,
  options?: Record<string, unknown>
) => {
  const map: Record<string, string> = {
    "services.validation.relayRequired": "At least one relay is required",
    "services.validation.explorerRequired": "Explorer URL is required.",
    "services.validation.invalidUrl": `${options?.field ?? "Field"}: invalid URL format`,
  };
  return map[key] ?? key;
};

export const validateServiceConfig = (
  state: ServiceConfigState,
  t: TFunction = fallbackT
): string[] => {
  const errors: string[] = [];

  if (!state.relay) {
    errors.push(t("services.validation.relayRequired"));
  } else {
    const relays = state.relay
      .split(",")
      .map((r) => r.trim())
      .filter((r) => r.length > 0);
    if (relays.length === 0) {
      errors.push(t("services.validation.relayRequired"));
    }
    for (const relayUrl of relays) {
      const relayErr = validateWebSocketURL(relayUrl, "Nostr Relay URL");
      if (relayErr) {
        errors.push(
          t("services.validation.invalidUrl", { field: "Nostr Relay URL" })
        );
        break;
      }
    }
  }

  const generalRelays = state.generalRelay
    .split(",")
    .map((r) => r.trim())
    .filter((r) => r.length > 0);
  for (const relayUrl of generalRelays) {
    const generalRelayErr = validateWebSocketURL(relayUrl, "General Relay URL");
    if (generalRelayErr) {
      errors.push(
        t("services.validation.invalidUrl", { field: "General Relay URL" })
      );
      break;
    }
  }

  const searchRelays = state.searchRelay
    .split(",")
    .map((r) => r.trim())
    .filter((r) => r.length > 0);
  for (const relayUrl of searchRelays) {
    const searchRelayErr = validateWebSocketURL(relayUrl, "Search Relay URL");
    if (searchRelayErr) {
      errors.push(
        t("services.validation.invalidUrl", { field: "Search Relay URL" })
      );
      break;
    }
  }

  if (!state.mempoolApi) {
    errors.push(t("services.validation.explorerRequired"));
  } else {
    const mempoolErr = validateHTTPURL(state.mempoolApi, "Explorer URL");
    if (mempoolErr)
      errors.push(
        t("services.validation.invalidUrl", { field: "Explorer URL" })
      );
  }

  // The app store is optional — empty just turns it off, so only the URL format
  // is checked when a value is present.
  if (state.lokihubStoreUrl) {
    const storeErr = validateHTTPURL(state.lokihubStoreUrl, "App Store URL");
    if (storeErr) {
      errors.push(
        t("services.validation.invalidUrl", { field: "App Store URL" })
      );
    }
  }

  return errors;
};

export function ServiceConfigForm({
  state,
  onChange,
  className,
  validationErrors = [],
  showGeneralRelay = true,
  showSearchRelay = true,
  showAppStore = true,
}: ServiceConfigFormProps) {
  const { t } = useTranslation("setup");

  const relayTabs = [
    { key: "nwc", label: t("services.relay.tabLabel"), icon: Zap },
    ...(showGeneralRelay
      ? [
          {
            key: "general",
            label: t("services.generalRelay.tabLabel"),
            icon: Users,
          },
        ]
      : []),
    ...(showSearchRelay
      ? [
          {
            key: "search",
            label: t("services.searchRelay.tabLabel"),
            icon: Search,
          },
        ]
      : []),
  ];

  const [communityOptions, setCommunityOptions] = useState<{
    relay: ServiceOption[];
    mempool: ServiceOption[];
    appStore: ServiceOption[];
  }>({
    relay: [],
    mempool: [],
    appStore: [],
  });

  useEffect(() => {
    async function fetchServices() {
      try {
        const services = await request<any>("/api/setup/config");
        if (services) {
          const mapUrlService = (s: any) => ({
            name: s.name,
            value: s.url,
            description: s.description,
          });

          setCommunityOptions({
            relay: (services.nostr_relay || []).map(mapUrlService),
            mempool: (services.flokicoin_explorer || []).map(mapUrlService),
            appStore: (services.lokihub_store || []).map(mapUrlService),
          });
        }
      } catch (error) {
        console.error("Failed to fetch community services", error);
      }
    }
    fetchServices();
  }, []);

  return (
    <div className={`space-y-6 ${className || ""}`}>
      <ServiceConfigurationHeader />

      {/* Flokicoin Explorer */}
      <Card className="border-border shadow-sm">
        <CardHeader className="pb-3">
          <CardTitle className="text-lg flex items-center gap-2">
            <Globe className="w-5 h-5 text-primary" />
            {t("services.explorer.title")}
          </CardTitle>
          <CardDescription>
            {t("services.explorer.description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ServiceCardSelector
            value={state.mempoolApi}
            onChange={(val) => onChange({ ...state, mempoolApi: val })}
            options={communityOptions.mempool}
            placeholder="https://..."
            customIcon={<Globe className="w-4 h-4" />}
            customLabel={t("services.explorer.custom")}
          />
        </CardContent>
      </Card>

      {/* Nostr Relays — grouped: Wallet Connect, General, Search each keep their own independent list */}
      <Card className="border-border shadow-sm">
        <CardHeader className="pb-3">
          <CardTitle className="text-lg flex items-center gap-2">
            <Zap className="w-5 h-5 text-primary" />
            {t("services.relayGroup.title")}
          </CardTitle>
          <CardDescription>
            {t("services.relayGroup.description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {relayTabs.length > 1 ? (
            <Tabs defaultValue={relayTabs[0].key}>
              <TabsList className="mb-4">
                {relayTabs.map(({ key, label, icon: Icon }) => (
                  <TabsTrigger key={key} value={key} className="gap-1.5">
                    <Icon className="w-3.5 h-3.5" />
                    {label}
                  </TabsTrigger>
                ))}
              </TabsList>

              <TabsContent value="nwc" className="space-y-3">
                <p className="text-sm text-muted-foreground">
                  {t("services.relay.description")}
                </p>
                <MultiRelayInput
                  value={state.relay}
                  onChange={(val) => onChange({ ...state, relay: val })}
                  options={communityOptions.relay}
                  placeholder="wss://..."
                />
                <div className="flex items-start justify-between gap-4 rounded-lg border border-border p-3">
                  <div className="space-y-1">
                    <p className="text-sm font-medium">
                      {t("services.trustedNwcRelay.title")}
                    </p>
                    <p className="text-sm text-muted-foreground">
                      {t("services.trustedNwcRelay.description")}
                    </p>
                  </div>
                  <Switch
                    checked={state.trustedNwcRelay}
                    onCheckedChange={(checked) =>
                      onChange({ ...state, trustedNwcRelay: checked })
                    }
                  />
                </div>
              </TabsContent>

              {showGeneralRelay && (
                <TabsContent value="general" className="space-y-3">
                  <p className="text-sm text-muted-foreground">
                    {t("services.generalRelay.description")}
                  </p>
                  <MultiRelayInput
                    value={state.generalRelay}
                    onChange={(val) =>
                      onChange({ ...state, generalRelay: val })
                    }
                    options={communityOptions.relay}
                    placeholder="wss://..."
                  />
                </TabsContent>
              )}

              {showSearchRelay && (
                <TabsContent value="search" className="space-y-3">
                  <p className="text-sm text-muted-foreground">
                    {t("services.searchRelay.description")}
                  </p>
                  <MultiRelayInput
                    value={state.searchRelay}
                    onChange={(val) => onChange({ ...state, searchRelay: val })}
                    options={communityOptions.relay}
                    placeholder="wss://..."
                  />
                </TabsContent>
              )}
            </Tabs>
          ) : (
            <MultiRelayInput
              value={state.relay}
              onChange={(val) => onChange({ ...state, relay: val })}
              options={communityOptions.relay}
              placeholder="wss://..."
            />
          )}
        </CardContent>
      </Card>

      {/* App Store — optional. Empty means OFF (no sync, no fetch); the
          raw.githubusercontent suggestion below is inactive until picked. */}
      {showAppStore && (
        <Card className="border-border shadow-sm">
          <CardHeader className="pb-3">
            <CardTitle className="text-lg flex items-center gap-2">
              <LayoutGrid className="w-5 h-5 text-primary" />
              {t("services.appStore.title")}
            </CardTitle>
            <CardDescription>
              {t("services.appStore.description")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ServiceCardSelector
              value={state.lokihubStoreUrl}
              onChange={(val) =>
                onChange({ ...state, lokihubStoreUrl: val })
              }
              options={communityOptions.appStore}
              placeholder="https://..."
              customIcon={<LayoutGrid className="w-4 h-4" />}
              customLabel={t("services.appStore.custom")}
            />
          </CardContent>
        </Card>
      )}

      {/* LSP catalogue (NIP-LSP announcements; WP-26). Shown in both setup and
          Settings; trusting an LSP is always optional, so setup stays
          skippable. */}
      <LSPCatalogue className="border-border shadow-sm" />

      {validationErrors.length > 0 && (
        <div id="service-config-errors" className="scroll-mt-4">
          <Alert
            variant="destructive"
            className="mt-6 w-full animate-in fade-in slide-in-from-bottom-2"
          >
            <AlertCircle className="h-4 w-4" />
            <AlertTitle>{t("services.configErrors")}</AlertTitle>
            <AlertDescription>
              <ul className="list-disc ps-5 space-y-1 mt-2">
                {validationErrors.map((err, i) => (
                  <li key={i}>{err}</li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        </div>
      )}
    </div>
  );
}
