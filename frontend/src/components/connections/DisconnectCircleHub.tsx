import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "src/components/ui/alert-dialog";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "src/components/ui/table";
import {
  App,
  CircleChildBalance,
  CircleDeleteMode,
  DeleteCircleHubResult,
  ListCircleChildrenBalancesResponse,
} from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

export function DisconnectCircleHub({
  app,
  onClose,
}: {
  app: App;
  onClose: () => void;
}) {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const { mutate } = useSWRConfig();

  const [children, setChildren] = React.useState<CircleChildBalance[] | null>(
    null
  );
  const [isDeleting, setDeleting] = React.useState(false);

  React.useEffect(() => {
    (async () => {
      try {
        const data = await request<ListCircleChildrenBalancesResponse>(
          `/api/apps/${app.id}/circle/children`
        );
        setChildren(data?.children ?? []);
      } catch (error) {
        handleRequestError(t("disconnectCircleHub.errors.load"), error);
        setChildren([]);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [app.id]);

  const performDelete = async (mode: CircleDeleteMode) => {
    setDeleting(true);
    try {
      const result = await request<DeleteCircleHubResult>(
        `/api/apps/${app.id}/circle/delete`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mode }),
        }
      );

      await mutate(
        (key) => typeof key === "string" && key.startsWith("/api/apps"),
        undefined,
        { revalidate: true }
      );

      if (result?.hubDeleted) {
        toast(t("disconnectCircleHub.deletedToast"));
        navigate("/sub-wallets");
        return;
      }

      toast(
        t("disconnectCircleHub.partialDeleteToast", {
          count: result?.deletedChildIds?.length ?? 0,
        }),
        {
          description: t("disconnectCircleHub.partialDeleteDescription", {
            count: result?.skippedChildIds?.length ?? 0,
          }),
        }
      );
      onClose();
    } catch (error) {
      handleRequestError(t("disconnectCircleHub.errors.delete"), error);
    } finally {
      setDeleting(false);
    }
  };

  const nonEmpty = (children ?? []).filter((c) => c.balanceMloki !== 0);
  const totalLoki = nonEmpty.reduce((sum, c) => sum + c.balanceMloki / 1000, 0);
  const isLoading = children === null;

  return (
    <AlertDialog open>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("disconnectCircleHub.title")}
          </AlertDialogTitle>
          <AlertDialogDescription asChild>
            {isLoading ? (
              <p>{t("disconnectCircleHub.checking")}</p>
            ) : nonEmpty.length === 0 ? (
              <p>{t("disconnectCircleHub.allEmpty")}</p>
            ) : (
              <div className="space-y-3">
                <p>
                  {t("disconnectCircleHub.hasBalance", {
                    count: nonEmpty.length,
                  })}
                </p>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("disconnectCircleHub.colName")}</TableHead>
                      <TableHead>
                        {t("disconnectCircleHub.colPubkey")}
                      </TableHead>
                      <TableHead className="text-end">
                        {t("disconnectCircleHub.colBalance")}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {nonEmpty.map((c) => (
                      <TableRow key={c.appId}>
                        <TableCell>{c.name}</TableCell>
                        <TableCell className="font-mono text-xs">
                          {c.appPubkey.slice(0, 12)}…{c.appPubkey.slice(-6)}
                        </TableCell>
                        <TableCell className="text-end">
                          {(c.balanceMloki / 1000).toLocaleString()}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
                <p className="text-sm text-muted-foreground">
                  {t("disconnectCircleHub.totalSummary", {
                    total: totalLoki.toLocaleString(),
                    count: nonEmpty.length,
                  })}
                </p>
              </div>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose} disabled={isDeleting}>
            {tc("actions.cancel")}
          </AlertDialogCancel>
          {!isLoading && nonEmpty.length > 0 && (
            <LoadingButton
              variant="outline"
              loading={isDeleting}
              onClick={() => performDelete("empty_only")}
            >
              {t("disconnectCircleHub.deleteEmptyOnly")}
            </LoadingButton>
          )}
          <LoadingButton
            loading={isDeleting}
            disabled={isLoading}
            onClick={() => performDelete("all")}
          >
            {t("disconnectCircleHub.deleteAll")}
          </LoadingButton>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
