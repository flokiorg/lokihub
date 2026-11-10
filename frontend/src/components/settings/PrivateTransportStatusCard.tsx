import { useState } from "react";
import { toast } from "sonner";
import { NodeIdentityRows } from "src/components/NodeIdentityRows";
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
import { useInfo } from "src/hooks/useInfo";
import { useNodeIdentity } from "src/hooks/useNodeIdentity";
import { InfoResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// PrivateTransportStatusCard shows whether this hub's NIP-CASH
// private-transport announcement (kind 11190) is actually reaching clients.
// It exists because a failure here is otherwise invisible from the UI: the
// hub keeps serving everything else normally (mint_cash, get_balance,
// pay_invoice), so "cash_status/cash_redeem/cash_transfer/cash_consolidate
// unreachable" only ever showed up as a client-side ErrNoAnnouncement, with
// nothing on this end to check.
export function PrivateTransportStatusCard({
  privateTransport,
  className,
}: {
  privateTransport: InfoResponse["privateTransport"];
  className?: string;
}) {
  const nodeIdentity = useNodeIdentity();
  const { mutate: reloadInfo } = useInfo();
  const [rotating, setRotating] = useState(false);

  // Rotate the inbox key (NIP-CASH §The Hub Announcement): the hub derives the
  // next inbox key and republishes the announcement, while the retired key keeps
  // opening envelopes for the request-freshness window so nothing in flight is
  // lost. This is the remedy for a leaked inbox key — without it, a leak would
  // expose all future private traffic for every bill in circulation.
  async function rotateInbox() {
    setRotating(true);
    try {
      await request("/api/private-transport/rotate-inbox", { method: "POST" });
      await reloadInfo();
      toast("Inbox key rotated. The new announcement has been published.");
    } catch (e) {
      handleRequestError("Could not rotate the inbox key", e);
    } finally {
      setRotating(false);
    }
  }

  return (
    <Card className={className}>
      <CardHeader className="pb-3">
        <CardTitle className="text-lg flex items-center gap-2">
          Private Transport
          <Badge
            variant={privateTransport.announced ? "positive" : "destructive"}
          >
            {privateTransport.announced ? "Announced" : "Not announced"}
          </Badge>
        </CardTitle>
        <CardDescription>
          The NIP-CASH private transport is what serves cash_status,
          cash_redeem, cash_transfer and cash_consolidate. New clients that
          haven't talked to this hub before can only reach it once the
          announcement below has been published.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {privateTransport.announcedRelays}/{privateTransport.totalRelays}{" "}
          configured relays accepted the announcement.
          {privateTransport.error && (
            <span className="block text-destructive mt-1">
              {privateTransport.error}
            </span>
          )}
        </p>
        <NodeIdentityRows identity={nodeIdentity} />
        {privateTransport.inboxPubkey && (
          <div className="grid gap-1">
            <span className="text-sm text-muted-foreground">
              Inbox pubkey (ephemeral transport key, not a profile identity)
            </span>
            <span className="font-mono text-sm truncate block">
              {privateTransport.inboxPubkey}
            </span>
          </div>
        )}
        <div className="flex flex-col gap-1">
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button variant="outline" size="sm" className="w-fit" disabled={rotating}>
                {rotating ? "Rotating…" : "Rotate inbox key"}
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Rotate the inbox key?</AlertDialogTitle>
                <AlertDialogDescription>
                  The hub will derive a new inbox key and publish a fresh
                  announcement. The old key keeps opening requests for the
                  freshness window, so nothing already in flight is lost. Rotate
                  if the current inbox key may have leaked.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction onClick={rotateInbox}>
                  Rotate inbox key
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
          <span className="text-xs text-muted-foreground">
            Replaces a leaked inbox key; the retired key stays valid for the
            request-freshness window.
          </span>
        </div>
      </CardContent>
    </Card>
  );
}
