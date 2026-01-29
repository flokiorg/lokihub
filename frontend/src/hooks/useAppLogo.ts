import { useEffect, useState } from "react";

export function useAppLogo(appId?: string) {
  const [logoSrc, setLogoSrc] = useState<string | undefined>(undefined);

  useEffect(() => {
    if (!appId) {
      setLogoSrc(undefined);
      return;
    }

    const loadLogo = async () => {
      // Check if running in Wails
      // @ts-ignore
      if (window.go && window.go.wails && window.go.wails.WailsApp) {
        try {
          // @ts-ignore
          const response = await window.go.wails.WailsApp.WailsRequestRouter(
            `/api/appstore/logos/${appId}`,
            "GET",
            ""
          );
          if (response.Error) {
            console.error("Failed to load logo from Wails:", response.Error);
            setLogoSrc(undefined);
          } else {
            setLogoSrc(`data:image/png;base64,${response.Body}`);
          }
        } catch (e) {
          console.error("Error calling WailsRequestRouter:", e);
          setLogoSrc(undefined);
        }
      } else {
        // Web environment
        setLogoSrc(`/api/appstore/logos/${appId}`);
      }
    };

    loadLogo();
  }, [appId]);

  return logoSrc;
}
