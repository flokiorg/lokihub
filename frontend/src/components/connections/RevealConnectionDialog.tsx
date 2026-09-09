import { useTranslation } from "react-i18next";
import { ConnectAppCard } from "src/screens/apps/ConnectAppCard";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { Badge } from "src/components/ui/badge";
import { useApp } from "src/hooks/useApp";
import { App } from "src/types";
import { formatClaimDeadline } from "src/utils/cashWallet";

// Shows a Cash wallet's pairing secret (deterministically re-derivable NWC
// URI). Always uses ConnectAppCard's bare "reveal" layout (no nested Card) so
// it doesn't double up on Dialog's own header/footer chrome. Two modes:
// - "reveal" (default): re-shows an already-existing connection's secret, with
//   no "waiting for connection" state — appropriate for a secret that may be
//   long since connected.
// - "create": shown right after a brand-new wallet was created — polls the
//   app (same as NewApp.tsx's FinalizeConnection) and turns on
//   ConnectAppCard's connection-status block ("Waiting for app to
//   connect..."/timeout/"App connected").
export function RevealConnectionDialog({
  app,
  pairingUri,
  lokicashToken,
  bearerSecret,
  walletSummary,
  mode = "reveal",
  primaryFormat = "nwc",
  onClose,
}: {
  app: App;
  pairingUri: string;
  // lokicashToken: the same connection as pairingUri, packaged as a
  // single lokicash1... string (NIP-CASH §The Lokicash Token) — optional
  // since not every app kind this dialog is reused for has one.
  lokicashToken?: string;
  // bearerSecret: only ever present right after creating a bearer-mode Cash
  // wallet (mode === "create") — the wallet mints it once and never returns
  // it again (NIP-CASH §Bearer Slices), so there is no "reveal" path for it.
  bearerSecret?: string;
  // walletSummary: a Cash wallet's totals — shown above the QR whenever
  // primaryFormat is "lokicash". Callers already have this from the claims
  // list they fetched to render their own row/card, so it's passed in
  // rather than re-derived here from a separate round trip.
  walletSummary?: {
    amountLoki: number;
    recipientCount: number;
    claimedCount: number;
    expiresAtSecs?: number;
  };
  mode?: "reveal" | "create";
  // "lokicash": Cash wallets — the dialog title and ConnectAppCard both drop
  // pairingUri entirely, showing only the lokicash1... token (see
  // ConnectAppCard's own primaryFormat doc comment). Requires lokicashToken.
  primaryFormat?: "nwc" | "lokicash";
  onClose: () => void;
}) {
  const { t } = useTranslation("apps");
  const { t: tj } = useTranslation("circles");
  const { data: polledApp } = useApp(mode === "create" ? app.id : undefined, true);

  const deadline = walletSummary?.expiresAtSecs
    ? formatClaimDeadline(walletSummary.expiresAtSecs)
    : undefined;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="mb-2">
            {primaryFormat === "lokicash"
              ? t("connectAppCard.lokicashTitle", "Lokicash")
              : t("connectAppCard.connectionSecret", "Connection Secret")}
          </DialogTitle>
        </DialogHeader>
        {primaryFormat === "lokicash" && walletSummary && (
          <div className="mb-4 grid gap-1.5 rounded-lg border p-3 text-sm">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">
                {t("connectAppCard.amountLabel", "Amount")}
              </span>
              <span className="font-medium tabular-nums">
                {walletSummary.amountLoki.toLocaleString()} loki
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">
                {t("connectAppCard.statusLabel", "Status")}
              </span>
              {walletSummary.claimedCount === walletSummary.recipientCount ? (
                <Badge variant="positive">
                  {tj("cashHubAllocations.tokenStatusFullyRedeemed")}
                </Badge>
              ) : walletSummary.claimedCount === 0 ? (
                <Badge variant="secondary">
                  {tj("cashHubAllocations.tokenStatusUnredeemed")}
                </Badge>
              ) : (
                <Badge variant="outline">
                  {tj("cashHubAllocations.tokenStatusPartiallyRedeemed", {
                    claimed: walletSummary.claimedCount,
                    count: walletSummary.recipientCount,
                  })}
                </Badge>
              )}
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">
                {t("connectAppCard.expiresLabel", "Expires")}
              </span>
              <span title={deadline?.title}>
                {deadline?.label ?? tj("claimDeadline.none")}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">
                {t("connectAppCard.recipientsLabel", "Recipients")}
              </span>
              <span className="font-medium tabular-nums">
                {walletSummary.recipientCount}
              </span>
            </div>
          </div>
        )}
        <ConnectAppCard
          app={mode === "create" ? (polledApp ?? app) : app}
          pairingUri={pairingUri}
          lokicashToken={lokicashToken}
          bearerSecret={bearerSecret}
          variant="reveal"
          showConnectionStatus={mode === "create"}
          primaryFormat={primaryFormat}
        />
      </DialogContent>
    </Dialog>
  );
}
