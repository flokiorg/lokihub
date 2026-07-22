import {
    AlertCircle,
    ArrowLeftRight,
    Globe,
    MessageCircle,
    Search,
    Users,
    Zap
} from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { LSPManagementCard } from "src/components/LSPManagementCard";
import { MultiRelayInput } from "src/components/MultiRelayInput";
import { ServiceCardSelector, ServiceOption } from "src/components/ServiceCardSelector";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "src/components/ui/tabs";
import { LSP } from "src/types";
import { request } from "src/utils/request";
import { validateHTTPURL, validateMessageBoardURL, validateWebSocketURL } from "src/utils/validation";

export interface ServiceConfigState {
    mempoolApi: string;
    relay: string;
    generalRelay: string;
    searchRelay: string;
    swapServiceUrl: string;
    messageboardNwcUrl: string;
    enableSwap: boolean;
    enableMessageboardNwc: boolean;
    lsps: LSP[];
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
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type TFunction = (key: any, options?: any) => string;

const fallbackT: TFunction = (key: string, options?: Record<string, unknown>) => {
    const map: Record<string, string> = {
        "services.validation.relayRequired": "At least one relay is required",
        "services.validation.explorerRequired": "Flokicoin Explorer URL is required.",
        "services.validation.swapRequired": "Swap Service URL is required when enabled.",
        "services.validation.messageboardRequired": "Messageboard URL is required when enabled.",
        "services.validation.invalidUrl": `${options?.field ?? "Field"}: invalid URL format`,
    };
    return map[key] ?? key;
};

export const validateServiceConfig = (state: ServiceConfigState, t: TFunction = fallbackT): string[] => {
    const errors: string[] = [];

    if (!state.relay) {
        errors.push(t("services.validation.relayRequired"));
    } else {
        const relays = state.relay.split(",").map(r => r.trim()).filter(r => r.length > 0);
        if (relays.length === 0) {
            errors.push(t("services.validation.relayRequired"));
        }
        for (const relayUrl of relays) {
            const relayErr = validateWebSocketURL(relayUrl, "Nostr Relay URL");
            if (relayErr) {
                errors.push(t("services.validation.invalidUrl", { field: "Nostr Relay URL" }));
                break;
            }
        }
    }

    const generalRelays = state.generalRelay.split(",").map(r => r.trim()).filter(r => r.length > 0);
    for (const relayUrl of generalRelays) {
        const generalRelayErr = validateWebSocketURL(relayUrl, "General Relay URL");
        if (generalRelayErr) {
            errors.push(t("services.validation.invalidUrl", { field: "General Relay URL" }));
            break;
        }
    }

    const searchRelays = state.searchRelay.split(",").map(r => r.trim()).filter(r => r.length > 0);
    for (const relayUrl of searchRelays) {
        const searchRelayErr = validateWebSocketURL(relayUrl, "Search Relay URL");
        if (searchRelayErr) {
            errors.push(t("services.validation.invalidUrl", { field: "Search Relay URL" }));
            break;
        }
    }

    if (!state.mempoolApi) {
        errors.push(t("services.validation.explorerRequired"));
    } else {
        const mempoolErr = validateHTTPURL(state.mempoolApi, "Flokicoin Explorer URL");
        if (mempoolErr) errors.push(t("services.validation.invalidUrl", { field: "Flokicoin Explorer URL" }));
    }

    if (state.enableSwap) {
        if (!state.swapServiceUrl) {
            errors.push(t("services.validation.swapRequired"));
        } else {
            const swapErr = validateHTTPURL(state.swapServiceUrl, "Swap Service URL");
            if (swapErr) errors.push(t("services.validation.invalidUrl", { field: "Swap Service URL" }));
        }
    }

    if (state.enableMessageboardNwc) {
        if (!state.messageboardNwcUrl) {
            errors.push(t("services.validation.messageboardRequired"));
        } else {
            const mbErr = validateMessageBoardURL(state.messageboardNwcUrl);
            if (mbErr) errors.push(t("services.validation.invalidUrl", { field: "Messageboard URL" }));
        }
    }

    return errors;
};

export function ServiceConfigForm({ state, onChange, className, validationErrors = [], showGeneralRelay = true, showSearchRelay = true }: ServiceConfigFormProps) {
    const { t } = useTranslation("setup");

    const relayTabs = [
        { key: "nwc", label: t("services.relay.tabLabel"), icon: Zap },
        ...(showGeneralRelay ? [{ key: "general", label: t("services.generalRelay.tabLabel"), icon: Users }] : []),
        ...(showSearchRelay ? [{ key: "search", label: t("services.searchRelay.tabLabel"), icon: Search }] : []),
    ];

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
        async function fetchServices() {
            try {
                const services = await request<any>("/api/setup/config");
                if (services) {
                    const mapUrlService = (s: any) => ({
                        name: s.name,
                        value: s.url,
                        description: s.description
                    });

                    const mapNwcService = (s: any) => ({
                        name: s.name,
                        value: s.nwc,
                        description: s.description
                    });

                    const mapLSP = (s: any) => {
                        const connection = s.connection || s.uri || "";
                        const parts = connection.split("@");
                        const pubkey = parts[0] || "";
                        const host = parts[1] || "";

                        return {
                            name: s.name,
                            pubkey: pubkey,
                            host: host,
                            active: false,
                            isCommunity: true,
                            description: s.description,
                            website: s.url || s.website
                        } as LSP;
                    };

                    setCommunityOptions({
                        swap: (services.swap_service || []).map(mapUrlService),
                        relay: (services.nostr_relay || []).map(mapUrlService),
                        messageboard: (services.messageboard_nwc || []).map(mapNwcService),
                        mempool: (services.flokicoin_explorer || []).map(mapUrlService),
                        lsp: (services.lsps || []).map(mapLSP),
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
                                <p className="text-sm text-muted-foreground">{t("services.relay.description")}</p>
                                <MultiRelayInput
                                    value={state.relay}
                                    onChange={(val) => onChange({ ...state, relay: val })}
                                    options={communityOptions.relay}
                                    placeholder="wss://..."
                                />
                            </TabsContent>

                            {showGeneralRelay && (
                                <TabsContent value="general" className="space-y-3">
                                    <p className="text-sm text-muted-foreground">{t("services.generalRelay.description")}</p>
                                    <MultiRelayInput
                                        value={state.generalRelay}
                                        onChange={(val) => onChange({ ...state, generalRelay: val })}
                                        placeholder="wss://..."
                                    />
                                </TabsContent>
                            )}

                            {showSearchRelay && (
                                <TabsContent value="search" className="space-y-3">
                                    <p className="text-sm text-muted-foreground">{t("services.searchRelay.description")}</p>
                                    <MultiRelayInput
                                        value={state.searchRelay}
                                        onChange={(val) => onChange({ ...state, searchRelay: val })}
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

            {/* LSP Management */}
            <LSPManagementCard
                localLSPs={state.lsps}
                setLocalLSPs={(lsps) => onChange({ ...state, lsps })}
                className="border-border shadow-sm"
            />

            {/* Swap Service */}
            <Card className="border-border shadow-sm">
                <CardHeader className="pb-3">
                    <div className="flex items-center justify-between">
                        <div className="space-y-1">
                            <CardTitle className="text-lg flex items-center gap-2">
                                <ArrowLeftRight className="w-5 h-5 text-primary" />
                                {t("services.swap.title")}
                            </CardTitle>
                            <CardDescription>
                                {t("services.swap.description")}
                            </CardDescription>
                        </div>
                        <Switch
                            checked={state.enableSwap}
                            onCheckedChange={(checked) => onChange({ ...state, enableSwap: checked })}
                        />
                    </div>
                </CardHeader>
                {state.enableSwap && (
                    <CardContent>
                        <ServiceCardSelector
                            value={state.swapServiceUrl}
                            onChange={(val) => onChange({ ...state, swapServiceUrl: val })}
                            options={communityOptions.swap}
                            placeholder="https://..."
                            disabled={!state.enableSwap}
                            customIcon={<ArrowLeftRight className="w-4 h-4" />}
                            customLabel={t("services.swap.custom")}
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
                                <MessageCircle className="w-5 h-5 text-primary" />
                                {t("services.messageboard.title")}
                            </CardTitle>
                            <CardDescription>
                                {t("services.messageboard.description")}
                            </CardDescription>
                        </div>
                        <Switch
                            checked={state.enableMessageboardNwc}
                            onCheckedChange={(checked) => onChange({ ...state, enableMessageboardNwc: checked })}
                        />
                    </div>
                </CardHeader>
                {state.enableMessageboardNwc && (
                    <CardContent>
                        <ServiceCardSelector
                            value={state.messageboardNwcUrl}
                            onChange={(val) => onChange({ ...state, messageboardNwcUrl: val })}
                            options={communityOptions.messageboard}
                            placeholder="nostr+walletconnect://..."
                            disabled={!state.enableMessageboardNwc}
                            customIcon={<MessageCircle className="w-4 h-4" />}
                            customLabel={t("services.messageboard.custom")}
                        />
                    </CardContent>
                )}
            </Card>

            {validationErrors.length > 0 && (
                <div id="service-config-errors" className="scroll-mt-4">
                    <Alert variant="destructive" className="mt-6 w-full animate-in fade-in slide-in-from-bottom-2">
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
