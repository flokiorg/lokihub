import { RotateCw } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
    ServiceConfigForm,
    ServiceConfigState,
    validateServiceConfig
} from "src/components/ServiceConfigForm";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardContent,
    CardFooter,
    CardHeader,
    CardTitle
} from "src/components/ui/card";
import { Badge } from "src/components/ui/badge";
import { AppError, request } from "src/utils/request";

type GlobalErrorProps = {
  error: Error;
};

export function GlobalError({ error }: GlobalErrorProps) {
  const { t } = useTranslation("common");
  const { t: tSetup } = useTranslation("setup");

  const isAppError = error instanceof AppError;
  const status = isAppError ? (error as AppError).status : undefined;
  const url = isAppError ? (error as AppError).url : undefined;
  const displayMessage = error.message;

  const helpText = !status
    ? "Cannot reach the server. Check your network connection or if the backend is running."
    : status === 401 || status === 403
      ? "Authentication failed. Your session may have expired — try reloading."
      : status >= 500
        ? "The server encountered an internal error. Check the backend logs."
        : undefined;
  const [loading, setLoading] = useState(false);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);
  
  const [config, setConfig] = useState<ServiceConfigState>({
      mempoolApi: "",
      relay: "",
      generalRelay: "",
      searchRelay: "",
      swapServiceUrl: "",
      messageboardNwcUrl: "",
      enableSwap: true,
      enableMessageboardNwc: true,
      lsps: [],
  });

  const [showConfig, setShowConfig] = useState(false);
  const [loadingConfig, setLoadingConfig] = useState(false);

  useEffect(() => {
    if (showConfig) {
      setLoadingConfig(true);
      
      const fetchInfo = request<import("src/types").InfoResponse>("/api/info");
      const fetchCommunity = request<any>("/api/setup/config");

      Promise.all([fetchInfo, fetchCommunity])
        .then(([info, community]) => {
            const communityLSPs = community?.lsps || [];
            
            if (info) {
                setConfig({
                  mempoolApi: info.mempoolUrl || "",
                  relay: info.relay || "",
                  generalRelay: info.generalRelay || "",
                  searchRelay: info.searchRelay || "",
                  swapServiceUrl: info.swapServiceUrl || "",
                  messageboardNwcUrl: info.messageboardNwcUrl || "",
                  enableSwap: info.enableSwap ?? true,
                  enableMessageboardNwc: info.enableMessageboardNwc ?? true,

                  lsps: (info.lsps || []).length > 0 ? info.lsps : communityLSPs.map((opt: any) => {
                        const [pubkeyRaw, host] = opt.uri?.split('@') || ['', ''];
                        return {
                            name: opt.name,
                            pubkey: pubkeyRaw,
                            host: host,
                            active: false,
                            isCommunity: true,
                            description: opt.description
                        } as import("src/types").LSP;
                  }),
                });
            }
        })
        .catch((e) => {
          console.error("Failed to fetch info", e);
          toast.error(t("criticalError.failedToLoad"));
        })
        .finally(() => {
          setLoadingConfig(false);
        });
    }
  }, [showConfig]);

  const saveConfiguration = async () => {
      setLoading(true);
      setValidationErrors([]);
      
      const errors = validateServiceConfig(config, tSetup);
      if (errors.length > 0) {
          setValidationErrors(errors);
          setLoading(false);
          setTimeout(() => {
              document.getElementById("service-config-errors")?.scrollIntoView({ behavior: "smooth", block: "center" });
          }, 100);
          return;
      }

      try {
        await request("/api/settings", {
            method: "PUT", // GlobalError used PUT previously, checking... Services used PATCH. Let's stick to consistent API if possible, but maybe PUT is needed here? Original used PUT.
            body: JSON.stringify({
              mempoolUrl: config.mempoolApi,
              swapServiceUrl: config.swapServiceUrl,
              messageboardNwcUrl: config.messageboardNwcUrl,
              relay: config.relay,
              generalRelay: config.generalRelay,
              searchRelay: config.searchRelay,
              enableSwap: config.enableSwap,
              enableMessageboardNwc: config.enableMessageboardNwc,
              lsps: config.lsps.map(l => ({
                  name: l.name,
                  pubkey: l.pubkey,
                  host: l.host,
                  active: l.active,
              })),
            }),
        });
        
        toast.success(t("criticalError.configUpdated"));
        setTimeout(() => window.location.reload(), 1000);
      } catch (error) {
          console.error(error);
          toast.error(t("criticalError.failedToUpdate"));
      } finally {
          setLoading(false);
      }
  };

  return (
    <div className="flex items-center justify-center min-h-screen p-4 bg-background overflow-y-auto">
      <Card className="w-full max-w-md my-8">
        <CardHeader>
          <CardTitle className="text-destructive">{t("criticalError.title")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-muted-foreground">
            {t("criticalError.description")}
          </p>
          <div className="space-y-3">
            {status && (
              <Badge variant="destructive">HTTP {status}</Badge>
            )}
            <div className="p-3 bg-muted rounded-md font-mono text-sm break-all max-h-40 overflow-y-auto">
              {displayMessage}
            </div>
            {url && (
              <p className="text-xs text-muted-foreground font-mono break-all">
                Endpoint: {url}
              </p>
            )}
            {helpText && (
              <p className="text-sm text-muted-foreground">{helpText}</p>
            )}
          </div>

          <div className="pt-4">
            <Button
              variant="outline"
              onClick={() => setShowConfig(!showConfig)}
              className="w-full"
            >
              {showConfig ? t("criticalError.hideConfig") : t("criticalError.updateConfig")}
            </Button>
          </div>

          {showConfig && (
            <div className="space-y-3 pt-2">
              {loadingConfig && <div className="text-sm text-muted-foreground">{t("criticalError.loadingConfig")}</div>}
              
              <ServiceConfigForm
                  state={config}
                  onChange={setConfig}
                  validationErrors={validationErrors}
                  className="space-y-4" 
              />
              
              {/* Note: ServiceConfigForm has its own Card wrappings, which might look nested inside this CardContent. 
                  However, GlobalError is a narrow Card (max-w-md).
                  ServiceConfigForm uses Cards for sections.
                  This might result in Card-in-Card UI which is okay, but `ServiceConfigForm` is designed for full page.
                  Given the constraints, it's acceptable.
               */}

              <Button onClick={saveConfiguration} disabled={loading} className="w-full mt-4">
                {loading ? <RotateCw className="w-4 h-4 me-2 animate-spin" /> : null}
                {loading ? t("actions.saving") : t("criticalError.saveReload")}
              </Button>
            </div>
          )}
        </CardContent>
        <CardFooter className="flex justify-end gap-2">
          {!showConfig && (
            <Button onClick={() => window.location.reload()}>
              <RotateCw className="w-4 h-4 me-2" />
              {t("criticalError.reloadApp")}
            </Button>
          )}
        </CardFooter>
      </Card>
    </div>
  );
}
