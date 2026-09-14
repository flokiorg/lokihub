import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import { App } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// messages lets a kind-specific caller (DisconnectCashHub, DisconnectApp's
// cash_wallet/circle_wallet branches, ...) report success/failure in terms
// of what was actually deleted ("Cash Wallet deleted") instead of the
// generic "Connection deleted" default, which only fits a real third-party
// app/isolated-wallet disconnect.
export function useDeleteApp(
  app: App,
  onSuccess?: () => void,
  messages?: { success?: string; error?: string }
) {
  const { t } = useTranslation("apps");
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

      toast(messages?.success ?? t("connections.deleteSuccess"));

      if (onSuccess) {
        onSuccess();
      }
    } catch (error) {
      await handleRequestError(
        messages?.error ?? t("connections.deleteError"),
        error
      );
    } finally {
      setDeleting(false);
    }
  }, [onSuccess, app, mutate, messages, t]);

  return React.useMemo(
    () => ({ deleteApp, isDeleting }),
    [deleteApp, isDeleting]
  );
}
