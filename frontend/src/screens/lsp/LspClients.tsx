import { useTranslation } from "react-i18next";
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
import { LspPeer } from "src/screens/lsp/types";
import { useFormatLoki } from "src/screens/lsp/useFormatLoki";
import { shortenMiddle } from "src/utils/nostr";

export function LspClients() {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  const { data: peers, error } = useLspPeers();

  // The shared LspLayout now renders the section header + tab bar above this
  // screen, so it no longer renders its own AppHeader.
  return (
    <div className="space-y-4">
      {error ? (
        <LspUnavailable error={error} />
      ) : !peers ? (
        <Skeleton className="h-48 w-full" />
      ) : peers.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {t("clients.empty")}
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-20">
                {t("clients.table.status")}
              </TableHead>
              <TableHead>{t("clients.table.peer")}</TableHead>
              <TableHead className="text-right">
                {t("clients.table.received")}
              </TableHead>
              <TableHead className="text-right">
                {t("clients.table.sent")}
              </TableHead>
              <TableHead className="text-right">
                {t("clients.table.orders")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {peers.map((p: LspPeer) => (
              <TableRow key={p.pub_key}>
                <TableCell>
                  <Badge variant={p.active ? "positive" : "secondary"}>
                    {p.active ? t("clients.online") : t("clients.offline")}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div className="grid gap-0.5">
                    <span className="truncate font-medium">
                      {p.alias || t("clients.unknownAlias")}
                    </span>
                    <span className="font-mono text-xs text-muted-foreground">
                      {shortenMiddle(p.pub_key)}
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
