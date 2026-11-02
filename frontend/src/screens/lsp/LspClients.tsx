import AppHeader from "src/components/AppHeader";
import { Badge } from "src/components/ui/badge";
import { Skeleton } from "src/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "src/components/ui/table";
import { useLspPeers } from "src/hooks/useLsp";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import { formatLoki, LspPeer } from "src/screens/lsp/types";

export function LspClients() {
  const { data: peers, error } = useLspPeers();

  return (
    <div className="space-y-4">
      <AppHeader
        title="Clients & Peers"
        description="Peers connected to this LSP node and their order activity"
      />
      {error ? (
        <LspUnavailable error={error} />
      ) : !peers ? (
        <Skeleton className="h-48 w-full" />
      ) : peers.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          No connected peers.
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-20">Status</TableHead>
              <TableHead>Peer</TableHead>
              <TableHead className="text-right">Received</TableHead>
              <TableHead className="text-right">Sent</TableHead>
              <TableHead className="text-right">Orders</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {peers.map((p: LspPeer) => (
              <TableRow key={p.pub_key}>
                <TableCell>
                  <Badge variant={p.active ? "positive" : "outline"}>
                    {p.active ? "Online" : "Offline"}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div className="grid gap-0.5">
                    <span className="truncate">{p.alias || "(unknown)"}</span>
                    <span className="font-mono text-xs text-muted-foreground break-all">
                      {p.pub_key}
                    </span>
                    {p.address && (
                      <span className="text-xs text-muted-foreground">
                        {p.address}
                      </span>
                    )}
                  </div>
                </TableCell>
                <TableCell className="text-right slashed-zero">
                  {formatLoki(p.in_loki)}
                </TableCell>
                <TableCell className="text-right slashed-zero">
                  {formatLoki(p.out_loki)}
                </TableCell>
                <TableCell className="text-right slashed-zero">
                  {p.order_count}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
