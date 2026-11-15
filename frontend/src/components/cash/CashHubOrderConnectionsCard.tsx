import { CopyIcon, EyeIcon, EyeOffIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  isOrderConnectionString,
  type OrderConnection,
} from "src/components/cash/orders";
import QRCode from "src/components/QRCode";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "src/components/ui/alert-dialog";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { useOrderConnections } from "src/hooks/useCashOrders";
import { copyToClipboard } from "src/lib/clipboard";
import { handleRequestError } from "src/utils/handleRequestError";
import { qrValue } from "src/utils/qrValue";
import { request } from "src/utils/request";

const when = (secs: number) => new Date(secs * 1000).toLocaleString();

// CashHubOrderConnectionsCard manages a Cash Hub's Order connections (NIP-CO /
// WP-19 item 8): create one with a label, re-show each as a `cashorder1…` string
// and QR (its secret is public), and retire one for good. It only ever shows a
// cashorder1 string — never the Hub's own cashhub1 pairing (H-CO-004).
export function CashHubOrderConnectionsCard({ hubId }: { hubId: number }) {
  const { t } = useTranslation("settings");
  const { data: conns, error, mutate } = useOrderConnections(hubId);
  const [label, setLabel] = useState("");
  const [creating, setCreating] = useState(false);

  if (error) {
    // Not a Cash Hub, or the endpoint is unavailable: nothing to manage.
    return null;
  }

  async function create() {
    setCreating(true);
    try {
      await request(`/api/cash-hubs/${hubId}/order-connections`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ label: label.trim() }),
      });
      setLabel("");
      await mutate();
      toast(t("sellCash.connections.createdToast"));
    } catch (e) {
      handleRequestError(t("sellCash.connections.createFailed"), e);
    } finally {
      setCreating(false);
    }
  }

  async function retire(connId: number) {
    try {
      await request(
        `/api/cash-hubs/${hubId}/order-connections/${connId}/retire`,
        { method: "POST" }
      );
      await mutate();
      toast(t("sellCash.connections.retiredToast"));
    } catch (e) {
      handleRequestError(t("sellCash.connections.retireFailed"), e);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("sellCash.connections.title")}</CardTitle>
        <CardDescription>
          {t("sellCash.connections.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="flex items-end gap-2">
          <div className="grid gap-1.5 flex-1">
            <Input
              placeholder={t("sellCash.connections.labelPlaceholder")}
              value={label}
              maxLength={255}
              onChange={(e) => setLabel(e.target.value)}
            />
          </div>
          <LoadingButton
            type="button"
            loading={creating}
            onClick={() => void create()}
          >
            {t("sellCash.connections.create")}
          </LoadingButton>
        </div>
        {conns && conns.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {t("sellCash.connections.empty")}
          </p>
        )}
        {conns?.map((c) => (
          <OrderConnectionRow
            key={c.id}
            conn={c}
            onRetire={() => void retire(c.id)}
          />
        ))}
      </CardContent>
    </Card>
  );
}

function OrderConnectionRow({
  conn,
  onRetire,
}: {
  conn: OrderConnection;
  onRetire: () => void;
}) {
  const { t } = useTranslation("settings");
  const [shown, setShown] = useState(false);
  const retired = conn.retiredAt != null;
  // H-CO-004: show a string only once it is confirmed to be a cashorder1 one.
  const signable =
    !!conn.connectionString && isOrderConnectionString(conn.connectionString);

  return (
    <div className="grid gap-2 rounded-md border p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">
          {conn.label || t("sellCash.connections.unlabeled")}
        </span>
        {retired && (
          <Badge variant="warning">{t("sellCash.connections.retired")}</Badge>
        )}
        <span className="ms-auto text-muted-foreground">
          {t("sellCash.connections.created")} {when(conn.createdAt)}
        </span>
      </div>
      {!signable ? (
        <p className="text-muted-foreground">
          {t("sellCash.connections.cannotSign")}
        </p>
      ) : (
        <>
          {shown && (
            <div className="grid gap-2 justify-items-center">
              <QRCode value={qrValue(conn.connectionString!)} />
              <code className="font-mono text-xs break-all text-center">
                {conn.connectionString}
              </code>
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setShown((v) => !v)}
            >
              {shown ? <EyeOffIcon /> : <EyeIcon />}
              {shown
                ? t("sellCash.connections.hide")
                : t("sellCash.connections.show")}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => copyToClipboard(conn.connectionString!)}
            >
              <CopyIcon />
              {t("sellCash.connections.copy")}
            </Button>
          </div>
        </>
      )}
      {!retired && (
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="w-fit text-destructive hover:text-destructive"
            >
              {t("sellCash.connections.retire")}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("sellCash.connections.retireTitle")}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t("sellCash.connections.retireWarning")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>
                {t("sellCash.connections.cancel")}
              </AlertDialogCancel>
              <AlertDialogAction onClick={onRetire}>
                {t("sellCash.connections.retireConfirm")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}
