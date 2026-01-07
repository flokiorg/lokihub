import React, { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import LottieLoading from "src/components/LottieLoading";
import { useInfo } from "src/hooks/useInfo";
import { saveAuthToken } from "src/lib/auth";
import useSetupStore from "src/state/SetupStore";
import { AuthTokenResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";
import { SetupLayout } from "./SetupLayout";

let lastStartupErrorTime: string;
export function SetupFinish() {
  const navigate = useNavigate();
  const { data: info, mutate: refetchInfo } = useInfo(true); // poll the info endpoint to auto-redirect when app is running

  const [loading, setLoading] = React.useState(false);
  // Removed connectionError state in favor of direct navigation on error
  const hasFetchedRef = React.useRef(false);

  const startupError = info?.startupError;
  const startupErrorTime = info?.startupErrorTime;

  React.useEffect(() => {
    // lastStartupErrorTime check is required because user may leave page and come back
    // after re-configuring settings
    if (
      startupError &&
      startupErrorTime &&
      startupErrorTime !== lastStartupErrorTime
    ) {
      lastStartupErrorTime = startupErrorTime;
      // Navigate back with error
      navigate("/setup/node", { 
        replace: true, 
        state: { error: startupError, step: "selection" } 
      });
    }
  }, [startupError, startupErrorTime]);

  useEffect(() => {
    if (!loading) {
      return;
    }
    const timer = setTimeout(() => {
      // SetupRedirect takes care of redirection once info.running is true
      // if it still didn't redirect after 30 seconds, we show an error
      // Typically initial startup should complete in less than 10 seconds.
      setLoading(false);
      navigate("/setup/node", { 
        replace: true, 
        state: { error: "Connection timed out. Please check your node.", step: "selection" } 
      });
    }, 30000);

    return () => {
      clearTimeout(timer);
    };
  }, [loading]);

  useEffect(() => {
    if (!info) {
      return;
    }
    // ensure setup call is only called once
    if (hasFetchedRef.current) {
      return;
    }
    hasFetchedRef.current = true;
    
    (async () => {
      setLoading(true);
      const result = await finishSetup(
        useSetupStore.getState().unlockPassword
      );
      // only setup call is successful as start is async
      if (!result.success) {
        setLoading(false);
        // Determine step based on config
        const nodeInfo = useSetupStore.getState().nodeInfo;
        let step = "selection";
        if (nodeInfo.backendType === "FLND") {
           if (nodeInfo.lndAddress) {
             step = "form";
           }
        }
        
        navigate("/setup/node", { 
          replace: true, 
          state: { 
            error: result.error || "Unknown error occurred",
            step,
          } 
        });
      } else {
        await refetchInfo();
        navigate("/setup/security", { replace: true });
      }
    })();
  }, [navigate, info, refetchInfo]);



  return (
    <SetupLayout>
      <div className="flex flex-col gap-5 justify-center text-center">
        <LottieLoading size={400} />
        <h1 className="font-semibold text-lg font-headline">
          Setting up your Hub...
        </h1>
      </div>
    </SetupLayout>
  );
}

const finishSetup = async (
  unlockPassword: string
): Promise<{ success: boolean; error?: string }> => {
  try {
    const nodeInfo = useSetupStore.getState().nodeInfo;

    // New endpoint logic for FLND
    if (nodeInfo.backendType === "FLND") {
      if (nodeInfo.autoConnect) {
        // Setup Local (Default)
        await request("/api/setup/local", {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify({
                unlockPassword,
                lokihubServicesURL: nodeInfo.lokihubServicesURL,
                swapServiceUrl: nodeInfo.swapServiceUrl,
                relay: nodeInfo.relay,
                messageboardNwcUrl: nodeInfo.messageboardNwcUrl,
                mempoolApi: nodeInfo.mempoolApi,
                // dataDir and rpcListen are removed from API
            }),
        });
      } else {
        // Setup Manual (Advanced)
        // Ensure values are strings not undefined
        const lndAddress = nodeInfo.lndAddress || "";
        const lndCertHex = nodeInfo.lndCertHex || "";
        const lndMacaroonHex = nodeInfo.lndMacaroonHex || "";
        
        await request("/api/setup/manual", {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify({
                unlockPassword,
                lndAddress,
                lndCertHex,
                lndMacaroonHex,
                lokihubServicesURL: nodeInfo.lokihubServicesURL,
                swapServiceUrl: nodeInfo.swapServiceUrl,
                relay: nodeInfo.relay,
                messageboardNwcUrl: nodeInfo.messageboardNwcUrl,
                mempoolApi: nodeInfo.mempoolApi,
            }),
        });
      }
    } else {
       await request("/api/setup", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          ...nodeInfo,
          unlockPassword,
        }),
      });
    }

    const authTokenResponse = await request<AuthTokenResponse>("/api/start", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        unlockPassword,
      }),
    });
    if (authTokenResponse) {
      saveAuthToken(authTokenResponse.token);
    }
    return { success: true };
  } catch (error: any) {
    handleRequestError("Failed to connect", error);
    return { success: false, error: error.message || "Failed to connect" };
  }
};

