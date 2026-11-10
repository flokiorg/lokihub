import { useTranslation } from "react-i18next";
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
  const { t } = useTranslation("lsp");
  const { data: peers, error } = useLspPeers();

  return (
    <div className="space-y-4">
      <AppHeader
        title={t("clients.title")}
        description={t("clients.description")}
      />
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
                  <Badge variant={p.active ? "positive" : "outline"}>
                    {p.active ? t("clients.online") : t("clients.offline")}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div className="grid gap-0.5">
                    <span className="truncate">
                      {p.alias || t("clients.unknownAlias")}
                    </span>
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
