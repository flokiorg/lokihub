import React from "react";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import { App } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

export function useDeleteApp(app: App, onSuccess?: () => void) {
  const [isDeleting, setDeleting] = React.useState(false);
  const { mutate } = useSWRConfig();

  const deleteApp = React.useCallback(async () => {
    setDeleting(true);
    try {
      // Delete the app/sub-wallet
      await request(`/api/apps/${app.id}`, {
        method: "DELETE",
        headers: {
          "Content-Type": "application/json",
        },
      });

      // Invalidate all /api/apps cache entries to force refetch
      await mutate(
        (key) => typeof key === "string" && key.startsWith("/api/apps"),
        undefined,
        { revalidate: true }
      );

      toast("Connection deleted");

      if (onSuccess) {
        onSuccess();
      }
    } catch (error) {
      await handleRequestError("Failed to delete connection", error);
    } finally {
      setDeleting(false);
    }
  }, [onSuccess, app, mutate]);

  return React.useMemo(
    () => ({ deleteApp, isDeleting }),
    [deleteApp, isDeleting]
  );
}
