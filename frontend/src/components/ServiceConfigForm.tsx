import {
    AlertCircle,
    Globe,
    MessageCircle,
    Server,
    Zap
} from "lucide-react";
import { useEffect, useState } from "react";
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
import { LSP } from "src/types";
import { request } from "src/utils/request";
import { validateHTTPURL, validateMessageBoardURL, validateWebSocketURL } from "src/utils/validation";

export interface ServiceConfigState {
    mempoolApi: string;
    relay: string;
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
}

export const validateServiceConfig = (state: ServiceConfigState): string[] => {
    const errors: string[] = [];

    // Validate relays
    if (!state.relay) {
        errors.push("At least one relay is required");
    } else {
        const relays = state.relay.split(",").map(r => r.trim()).filter(r => r.length > 0);
        if (relays.length === 0) {
            errors.push("At least one relay is required");
        }
        for (const relayUrl of relays) {
            const relayErr = validateWebSocketURL(relayUrl, "Nostr Relay URL");
            if (relayErr) {
                errors.push(relayErr);
                break; // Only show first error
            }
        }
    }

    // Validate mempool
    if (!state.mempoolApi) {
        errors.push("Flokicoin Explorer URL is required.");
    } else {
        const mempoolErr = validateHTTPURL(state.mempoolApi, "Flokicoin Explorer URL");
        if (mempoolErr) errors.push(mempoolErr);
    }

    // Validate swap
    if (state.enableSwap) {
        if (!state.swapServiceUrl) {
            errors.push("Swap Service URL is required when enabled.");
        } else {
            const swapErr = validateHTTPURL(state.swapServiceUrl, "Swap Service URL");
            if (swapErr) errors.push(swapErr);
        }
    }

    // Validate messageboard
    if (state.enableMessageboardNwc) {
        if (!state.messageboardNwcUrl) {
            errors.push("Messageboard URL is required when enabled.");
        } else {
            const mbErr = validateMessageBoardURL(state.messageboardNwcUrl);
            if (mbErr) errors.push(mbErr);
        }
    }

    return errors;
};

export function ServiceConfigForm({ state, onChange, className, validationErrors = [] }: ServiceConfigFormProps) {
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
                    setCommunityOptions({
                        swap: services.swap_service || [],
                        relay: services.nostr_relay || [],
                        messageboard: services.messageboard_nwc || [],
                        mempool: services.flokicoin_explorer || [],
                        lsp: services.lsps || [],
                    });
                }
            } catch (error) {
                console.error("Failed to fetch community services", error);
                // toast.error("Failed to fetch community services"); // Optional: suppress toast to avoid noise on simple load failures
            }
        }
        fetchServices();
    }, []);

    // Merge logic for LSPs:
    // This effect ensures that if we have community LSPs and backend LSPs (in state), they are merged correctly
    // so that the LSPManagementCard sees "isCommunity" flags and descriptions.
    // However, since `state.lsps` is managed by the parent, we should perhaps run this merge logic ONCE when community options load?
    // OR we just pass the raw community options to LSPManagementCard?
    // LSPManagementCard takes `localLSPs` which is the state.
    // Let's replicate the merge logic here effectively by updating the state?
    // NO, repeatedly updating parent state from child effect is bad.
    // Ideally LSPManagementCard should take `communityLSPs` as a prop and handle the merging/display logic?
    // But LSPManagementCard is already built to take `localLSPs`.
    // Let's assume the parent handles the initial merge (as they do now) or we assume basic LSP management here.
    // Actually, `ServiceConfigForm` is replacing the CARDs.
    // The previous parents did the merge logic.
    // Let's leave the complex merge logic to the parent for now, or assume `state.lsps` is already populated.
    // BUT `SetupServices` and `Services` did the merge logic inside their component.
    // If we want to deduplicate that, we should probably export a helper `mergeCommunityLSPs`.

    return (
        <div className={`space-y-6 ${className || ""}`}>
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
                        value={state.mempoolApi}
                        onChange={(val) => onChange({ ...state, mempoolApi: val })}
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
                        value={state.relay}
                        onChange={(val) => onChange({ ...state, relay: val })}
                        options={communityOptions.relay}
                        placeholder="wss://..."
                    />
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
                                <Server className="w-5 h-5 text-primary" />
                                Swap Service
                            </CardTitle>
                            <CardDescription>
                                Enables Lightning to on-chain swaps (and vice-versa).
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
                                Messageboard
                            </CardTitle>
                            <CardDescription>
                                Connects to a NWC-enabled messageboard service.
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
                        />
                    </CardContent>
                )}
            </Card>



            {validationErrors.length > 0 && (
                <div id="service-config-errors" className="scroll-mt-4">
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
    );
}

// Helper to merge community LSPs with existing LSPs
export function mergeLSPs(existingLSPs: LSP[], communityLSPConfig: ServiceOption[]): LSP[] {
    if (!communityLSPConfig) return existingLSPs;
    
    const communityCards = communityLSPConfig.map(opt => {
        const [pubkey, host] = opt.uri?.split('@') || ['', ''];
        const existing = existingLSPs.find(l => l.pubkey === pubkey);
        
        if (existing) {
            return {
                ...existing,
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
        } as LSP;
    });
    
    const communityPubkeys = new Set(communityCards.map(c => c.pubkey));
    const customCards = existingLSPs.filter(l => !communityPubkeys.has(l.pubkey));
    
    return [...communityCards, ...customCards];
}
