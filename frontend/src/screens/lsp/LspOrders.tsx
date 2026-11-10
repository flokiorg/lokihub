import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import AppHeader from "src/components/AppHeader";
import { Badge } from "src/components/ui/badge";
import { Input } from "src/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { Skeleton } from "src/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "src/components/ui/table";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "src/components/ui/tabs";
import { useLspJitChannels, useLspOrders } from "src/hooks/useLsp";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import {
  formatLoki,
  LspJitChannel,
  LspOrder,
  orderStateVariant,
} from "src/screens/lsp/types";

const STATES = ["ALL", "CREATED", "PAID", "COMPLETED", "FAILED", "EXPIRED"];

export function LspOrders() {
  const { t } = useTranslation("lsp");
  return (
    <div className="space-y-4">
      <AppHeader
        title={t("orders.title")}
        description={t("orders.description")}
      />
      <Tabs defaultValue="orders">
        <TabsList>
          <TabsTrigger value="orders">{t("orders.tabs.orders")}</TabsTrigger>
          <TabsTrigger value="jit">{t("orders.tabs.jit")}</TabsTrigger>
        </TabsList>
        <TabsContent value="orders" className="mt-4">
          <OrdersTab />
        </TabsContent>
        <TabsContent value="jit" className="mt-4">
          <JitTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function OrdersTab() {
  const { t } = useTranslation("lsp");
  const { data: orders, error } = useLspOrders();
  const [query, setQuery] = React.useState("");
  const [state, setState] = React.useState("ALL");
  const navigate = useNavigate();

  if (error) {
    return <LspUnavailable error={error} />;
  }
  if (!orders) {
    return <Skeleton className="h-48 w-full" />;
  }

  const filtered = orders.filter((o) => {
    if (state !== "ALL" && o.state !== state) {
      return false;
    }
    if (!query) {
      return true;
    }
    const q = query.toLowerCase();
    return (
      o.order_id.toLowerCase().includes(q) ||
      o.peer_id.toLowerCase().includes(q) ||
      (o.peer_alias ?? "").toLowerCase().includes(q)
    );
  });

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          placeholder={t("orders.filter.placeholder")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="max-w-sm"
        />
        <Select value={state} onValueChange={setState}>
          <SelectTrigger className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {STATES.map((s) => (
              <SelectItem key={s} value={s}>
                {s === "ALL" ? t("orders.filter.allStates") : s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="text-xs text-muted-foreground">
          {t("orders.count", {
            filtered: filtered.length,
            total: orders.length,
          })}
        </span>
      </div>

      {filtered.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {t("orders.empty")}
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("orders.table.order")}</TableHead>
              <TableHead>{t("orders.table.peer")}</TableHead>
              <TableHead>{t("orders.table.state")}</TableHead>
              <TableHead className="text-right">
                {t("orders.table.amount")}
              </TableHead>
              <TableHead className="text-right">
                {t("orders.table.fee")}
              </TableHead>
              <TableHead>{t("orders.table.created")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((o: LspOrder) => (
              <TableRow
                key={o.order_id}
                className="cursor-pointer"
                onClick={() =>
                  navigate(`/lsp/orders/${encodeURIComponent(o.order_id)}`)
                }
              >
                <TableCell className="font-mono text-xs">
                  {o.order_id.slice(0, 12)}…
                </TableCell>
                <TableCell className="max-w-[12rem] truncate">
                  {o.peer_alias || o.peer_id.slice(0, 16) + "…"}
                </TableCell>
                <TableCell>
                  <Badge variant={orderStateVariant(o.state)}>{o.state}</Badge>
                </TableCell>
                <TableCell className="text-right slashed-zero">
                  {formatLoki(o.amount)}
                </TableCell>
                <TableCell className="text-right slashed-zero">
                  {formatLoki(o.fee)}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {new Date(o.created_at).toLocaleString()}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function JitTab() {
  const { t } = useTranslation("lsp");
  const { data: channels, error } = useLspJitChannels();
  if (error) {
    return <LspUnavailable error={error} />;
  }
  if (!channels) {
    return <Skeleton className="h-48 w-full" />;
  }
  if (channels.length === 0) {
    return (
      <p className="py-8 text-center text-sm text-muted-foreground">
        {t("orders.jit.empty")}
      </p>
    );
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("orders.table.order")}</TableHead>
          <TableHead>{t("orders.jit.scid")}</TableHead>
          <TableHead>{t("orders.table.peer")}</TableHead>
          <TableHead className="text-right">
            {t("orders.jit.capacity")}
          </TableHead>
          <TableHead>{t("orders.jit.status")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {channels.map((c: LspJitChannel) => (
          <TableRow key={c.order_id}>
            <TableCell className="font-mono text-xs">
              {c.order_id.slice(0, 12)}…
            </TableCell>
            <TableCell className="font-mono text-xs">
              {c.channel_id || "—"}
            </TableCell>
            <TableCell className="max-w-[12rem] truncate">
              {c.peer_id.slice(0, 16)}…
            </TableCell>
            <TableCell className="text-right slashed-zero">
              {formatLoki(c.capacity)}
            </TableCell>
            <TableCell>
              <Badge variant={c.status === "ACTIVE" ? "positive" : "outline"}>
                {c.status}
              </Badge>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
