import { useEffect } from "react";

export function useAppLogo(appId?: string) {
  const url = appId ? `/api/appstore/logos/${appId}` : undefined;

  useEffect(() => {
    if (!url) return;
    const img = new Image();
    img.src = url;
  }, [url]);

  return url;
}
