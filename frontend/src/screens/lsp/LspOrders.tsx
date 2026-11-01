import React from "react";
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
  return (
    <div className="space-y-4">
      <AppHeader
        title="Channels & Orders"
        description="LSPS1 orders and the JIT channels they funded"
      />
      <Tabs defaultValue="orders">
        <TabsList>
          <TabsTrigger value="orders">Orders</TabsTrigger>
          <TabsTrigger value="jit">JIT channels</TabsTrigger>
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
          placeholder="Filter by order id, peer pubkey or alias"
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
                {s === "ALL" ? "All states" : s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="text-xs text-muted-foreground">
          {filtered.length} of {orders.length}
        </span>
      </div>

      {filtered.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          No orders match.
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Order</TableHead>
              <TableHead>Peer</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="text-right">Amount</TableHead>
              <TableHead className="text-right">Fee</TableHead>
              <TableHead>Created</TableHead>
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
        No funded JIT channels yet.
      </p>
    );
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Order</TableHead>
          <TableHead>SCID</TableHead>
          <TableHead>Peer</TableHead>
          <TableHead className="text-right">Capacity</TableHead>
          <TableHead>Status</TableHead>
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
