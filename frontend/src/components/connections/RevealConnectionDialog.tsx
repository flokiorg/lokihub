import { useTranslation } from "react-i18next";
import { ConnectAppCard } from "src/screens/apps/ConnectAppCard";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import { useApp } from "src/hooks/useApp";
import { App } from "src/types";

// Shows a JIT wallet's pairing secret (deterministically re-derivable NWC
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
  mode = "reveal",
  onClose,
}: {
  app: App;
  pairingUri: string;
  // lokicashToken: the same connection as pairingUri, packaged as a
  // single lokicash1... string (NIP-JW §The Lokicash Token) — optional
  // since not every app kind this dialog is reused for has one.
  lokicashToken?: string;
  // bearerSecret: only ever present right after creating a bearer-mode JIT
  // wallet (mode === "create") — the wallet mints it once and never returns
  // it again (NIP-JW §Bearer Slices), so there is no "reveal" path for it.
  bearerSecret?: string;
  mode?: "reveal" | "create";
  onClose: () => void;
}) {
  const { t } = useTranslation("apps");
  const { data: polledApp } = useApp(mode === "create" ? app.id : undefined, true);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="mb-2">
            {t("connectAppCard.connectionSecret", "Connection Secret")}
          </DialogTitle>
        </DialogHeader>
        <ConnectAppCard
          app={mode === "create" ? (polledApp ?? app) : app}
          pairingUri={pairingUri}
          lokicashToken={lokicashToken}
          bearerSecret={bearerSecret}
          variant="reveal"
          showConnectionStatus={mode === "create"}
        />
      </DialogContent>
    </Dialog>
  );
}
