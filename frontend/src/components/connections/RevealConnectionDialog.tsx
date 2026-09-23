import { useTranslation } from "react-i18next";
import { ConnectAppCard } from "src/screens/apps/ConnectAppCard";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
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
// Both the polling and the connection-status block are skipped entirely when
// primaryFormat is "lokicash", regardless of mode: a Cash token has no NWC
// pairing handshake for a recipient to "connect" through, so there is
// nothing to poll for and nothing to report as waiting/connected.
export function RevealConnectionDialog({
  app,
  pairingUri,
  lokicashToken,
  cashSecret,
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
  // cashSecret: only ever present right after creating a cash-mode Cash
  // wallet (mode === "create") — the wallet mints it once and never returns
  // it again (NIP-CASH §Cash-Mode Slices), so there is no "reveal" path for it.
  cashSecret?: string;
  // walletSummary: a Cash wallet's totals — shown above the QR whenever
  // primaryFormat is "lokicash". Callers already have this from the claims
  // list they fetched to render their own row/card, so it's passed in
  // rather than re-derived here from a separate round trip.
  walletSummary?: {
    amountLoki: number;
    recipientCount: number;
    claimedCount: number;
    expiresAtSecs?: number;
    // isCash: true when this wallet's one slice is cash mode (NIP-CASH
    // §Cash-Mode Slices — a cash-mode slice's wallet is always single-recipient).
    // Hides the Recipients row below: "Recipients: 1" doesn't mean anything
    // for a cash note presented as a single cash bill, the way it does
    // for a Cash Hub's shared, identity-bound wallet.
    isCash?: boolean;
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
  const shouldPollForConnection = mode === "create" && primaryFormat !== "lokicash";
  const { data: polledApp } = useApp(
    shouldPollForConnection ? app.id : undefined,
    true
  );

  const deadline = walletSummary?.expiresAtSecs
    ? formatClaimDeadline(walletSummary.expiresAtSecs)
    : undefined;

  // A freshly-minted cash secret is shown exactly this once and can never
  // be retrieved again (NIP-CASH §Cash-Mode Slices) — the Hub only ever stores
  // its hash. Dismissing this dialog without having copied it first means
  // the funds it guards are permanently unredeemable, same as losing a
  // physical cash bill. Block every accidental-dismiss path (backdrop
  // click, Escape, the corner "x") and require an explicit acknowledgment
  // instead, only for this one case — every other use of this dialog shows
  // a re-derivable connection (§The Pairing Connection), safe to dismiss
  // freely.
  const requiresSaveConfirmation = mode === "create" && Boolean(cashSecret);

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !requiresSaveConfirmation) {
          onClose();
        }
      }}
    >
      <DialogContent
        className="sm:max-w-md"
        showCloseButton={!requiresSaveConfirmation}
        onInteractOutside={(e) => {
          if (requiresSaveConfirmation) {
            e.preventDefault();
          }
        }}
        onEscapeKeyDown={(e) => {
          if (requiresSaveConfirmation) {
            e.preventDefault();
          }
        }}
      >
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
            {!walletSummary.isCash && (
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">
                  {t("connectAppCard.recipientsLabel", "Recipients")}
                </span>
                <span className="font-medium tabular-nums">
                  {walletSummary.recipientCount}
                </span>
              </div>
            )}
          </div>
        )}
        <ConnectAppCard
          app={shouldPollForConnection ? (polledApp ?? app) : app}
          pairingUri={pairingUri}
          lokicashToken={lokicashToken}
          cashSecret={cashSecret}
          variant="reveal"
          showConnectionStatus={shouldPollForConnection}
          primaryFormat={primaryFormat}
        />
        {requiresSaveConfirmation && (
          <DialogFooter>
            <Button onClick={onClose} className="w-full">
              {t(
                "connectAppCard.cashSavedConfirm",
                "I've saved this — Close"
              )}
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  );
}
