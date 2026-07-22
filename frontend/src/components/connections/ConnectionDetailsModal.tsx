import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { AlertCircleIcon, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "src/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { copyToClipboard } from "src/lib/clipboard";
import { App } from "src/types";

dayjs.extend(relativeTime);

function DetailRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid gap-1">
      <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      <div className="text-sm">{children}</div>
    </div>
  );
}

function CopyableValue({ value }: { value: string | number }) {
  return (
    <button
      type="button"
      onClick={() => copyToClipboard(String(value))}
      className="flex w-full items-center gap-2 rounded-md border bg-muted/30 px-3 py-2 text-start font-mono text-sm break-all hover:bg-muted/60"
    >
      <span className="break-all">{value}</span>
      <Copy className="ms-auto h-3.5 w-3.5 shrink-0 text-muted-foreground" />
    </button>
  );
}

export function ConnectionDetailsModal({
  app,
  onClose,
}: {
  app: App;
  onClose: () => void;
}) {
  const { t } = useTranslation("apps");
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("connectionDetailsModal.title")}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-4">
          <DetailRow label={t("connectionDetailsModal.appId")}>
            <CopyableValue value={app.id} />
          </DetailRow>

          <DetailRow label={t("connectionDetailsModal.appPubkey")}>
            <CopyableValue value={app.appPubkey} />
          </DetailRow>

          <DetailRow label={t("connectionDetailsModal.walletPubkey")}>
            <div className="flex items-center gap-2">
              <CopyableValue value={app.walletPubkey} />
              {!app.uniqueWalletPubkey && (
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <AlertCircleIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                    </TooltipTrigger>
                    <TooltipContent>
                      {t("connectionDetailsModal.noUniqueWalletPubkey")}
                    </TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              )}
            </div>
          </DetailRow>

          <div className="grid grid-cols-2 gap-4">
            <DetailRow label={t("connectionDetailsModal.lastUsed")}>
              <p
                className="text-muted-foreground"
                title={
                  app.lastUsedAt
                    ? dayjs(app.lastUsedAt).format("D MMMM YYYY, HH:mm")
                    : undefined
                }
              >
                {app.lastUsedAt
                  ? dayjs(app.lastUsedAt).fromNow()
                  : t("connectionDetailsModal.never")}
              </p>
            </DetailRow>

            <DetailRow label={t("connectionDetailsModal.created")}>
              <p
                className="text-muted-foreground"
                title={dayjs(app.createdAt).format("D MMMM YYYY, HH:mm")}
              >
                {dayjs(app.createdAt).fromNow()}
              </p>
            </DetailRow>
          </div>

          {app.metadata && (
            <DetailRow label={t("connectionDetailsModal.metadata")}>
              <pre className="overflow-x-auto rounded-md border bg-muted/30 p-3 font-mono text-xs text-muted-foreground">
                {JSON.stringify(app.metadata, null, 2)}
              </pre>
            </DetailRow>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("connectionDetailsModal.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
