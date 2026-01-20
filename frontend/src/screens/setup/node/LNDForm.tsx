import { CheckCircle2, InfoIcon, XCircle } from "lucide-react";
import React, { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { Button } from "src/components/ui/button";
import {
    Card,
    CardDescription,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import useSetupStore from "src/state/SetupStore";
import { request } from "src/utils/request";
import { SetupLayout } from "../SetupLayout";

type Step = "selection" | "form";

export function LNDForm() {
  const navigate = useNavigate();
  const location = useLocation();
  const setupStore = useSetupStore();
  // Derive step purely from location state.
  // If undefined, it defaults to "selection".
  const step: Step = location.state?.step || "selection";
  const [isHovered, setIsHovered] = useState<string | null>(null);

  const [lndAddress, setLndAddress] = React.useState<string>(
    setupStore.nodeInfo.lndAddress || ""
  );
  const [lndCertHex, setLndCertHex] = React.useState<string>(
    setupStore.nodeInfo.lndCertHex || ""
  );
  const [lndMacaroonHex, setLndMacaroonHex] = React.useState<string>(
    setupStore.nodeInfo.lndMacaroonHex || ""
  );

  const [setupStatus, setSetupStatus] = useState<{ active: boolean } | null>(
    null
  );

  const checkStatus = React.useCallback(async () => {
    try {
      const res = await request<{ active: boolean }>("/api/setup/status", {
        method: "GET",
        headers: {
          "Content-Type": "application/json",
        },
      });
      setSetupStatus(res || { active: false });
    } catch (e) {
      setSetupStatus({ active: false });
    }
  }, []);

  useEffect(() => {
    if (step === "selection") {
      checkStatus();
      const interval = setInterval(checkStatus, 5000);
      return () => clearInterval(interval);
    }
  }, [step, checkStatus]);

  async function handleSubmit(data: object) {
    setupStore.updateNodeInfo({
      backendType: "FLND",
      ...data,
    });
    navigate("/setup/finish", { replace: true });
  }

  function onAdvancedSubmit(e: React.FormEvent) {
    e.preventDefault();
    handleSubmit({
      lndAddress: lndAddress.replace(/\s/g, ""),
      lndCertHex: lndCertHex.replace(/\s/g, ""),
      lndMacaroonHex: lndMacaroonHex.replace(/\s/g, ""),
      autoConnect: false,
    });
  }

  const handleDefaultConnect = () => {
    handleSubmit({
      autoConnect: true,
      lndAddress: undefined,
      lndCertHex: undefined,
      lndMacaroonHex: undefined,
    });
  };

  if (step === "selection") {
    return (
      <SetupLayout 
        backTo="/setup/services"
      >
          <TwoColumnLayoutHeader
            title="Connect to FLND"
            description="Choose how you want to connect to your Flokicoin Lightning Network Daemon (FLND) node."
          />
          <div className="grid gap-4 mt-6 w-full">
          <Card
            className={`cursor-pointer transition-all ${
              isHovered === "default" ? "border-primary shadow-md" : ""
            }`}
            onClick={handleDefaultConnect}
            onMouseEnter={() => setIsHovered("default")}
            onMouseLeave={() => setIsHovered(null)}
          >
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                Local Node
                {setupStatus && (
                  <span
                    className="flex items-center text-xs font-normal ml-auto"
                    title={
                      setupStatus.active ? "Node is active" : "Node is unreachable"
                    }
                  >
                    {setupStatus.active ? (
                      <CheckCircle2 className="w-4 h-4 text-green-500 mr-1" />
                    ) : (
                      <XCircle className="w-4 h-4 text-red-500 mr-1" />
                    )}
                    {setupStatus.active ? "Ready" : "Offline"}
                  </span>
                )}
              </CardTitle>
              <CardDescription>
                Automatically connect to the standard FLND node running on this
                device. Recommended for most users.
              </CardDescription>
            </CardHeader>
          </Card>

          <Card
            className={`cursor-pointer transition-all ${
              isHovered === "advanced" ? "border-primary shadow-md" : ""
            }`}
            onClick={() => {
              // Push a new history entry so "Back" works
              navigate(".", { state: { step: "form" } });
            }}
            onMouseEnter={() => setIsHovered("advanced")}
            onMouseLeave={() => setIsHovered(null)}
          >
            <CardHeader>
              <CardTitle>Advanced Mode</CardTitle>
              <CardDescription>
                Manually enter your node&apos;s GRPC address, Admin Macaroon, and TLS
                Certificate. Suitable for remote connections.
              </CardDescription>
            </CardHeader>
          </Card>
          </div>
      </SetupLayout>
    );
  }

  return (
    <SetupLayout
      onBack={() => {
        navigate(".", { state: { step: "selection" }, replace: true });
      }}
    >
      <form className="flex flex-col items-center w-full" onSubmit={onAdvancedSubmit}>
        <div className="grid gap-4 w-full">
          <TwoColumnLayoutHeader
            title="Validating Connection"
            description="Enter your FLND connection details manually."
          />

          <div className="grid gap-1.5">
            <Label htmlFor="lnd-address">FLND Address (GRPC)</Label>
            <Input
              required
              name="lnd-address"
              onChange={(e) => setLndAddress(e.target.value)}
              value={lndAddress}
              id="lnd-address"
              autoComplete="off"
            />
          </div>
          
          <div className="grid gap-1.5">
            <Label htmlFor="lnd-macaroon-hex">Admin Macaroon (Hex)</Label>
            <Input
              required
              name="lnd-macaroon-hex"
              onChange={(e) => setLndMacaroonHex(e.target.value)}
              value={lndMacaroonHex}
              type="text"
              id="lnd-macaroon-hex"
              autoComplete="off"
            />
          </div>
          
          <div className="grid gap-1.5">
            <Label htmlFor="lnd-cert-hex">TLS Certificate (Hex) (optional)</Label>
            <Input
              name="lnd-cert-hex"
              onChange={(e) => setLndCertHex(e.target.value)}
              value={lndCertHex}
              type="text"
              id="lnd-cert-hex"
              autoComplete="off"
            />
            {!lndCertHex && (
              <div className="flex flex-row gap-2 items-center justify-start text-sm text-muted-foreground mt-2">
                <InfoIcon className="h-4 w-4 shrink-0" />
                Skipping TLS certificate is not recommended as it may expose your
                connection to security risks
              </div>
            )}
          </div>
          
          <div className="flex justify-end">
            <Button>Next</Button>
          </div>
        </div>
      </form>
    </SetupLayout>
  );
}
