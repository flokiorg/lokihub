import { NodeIdentityRows } from "src/components/NodeIdentityRows";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { useNodeIdentity } from "src/hooks/useNodeIdentity";
import { InfoResponse } from "src/types";

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
      </CardContent>
    </Card>
  );
}
