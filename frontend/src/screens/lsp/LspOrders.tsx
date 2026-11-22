import { SearchIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
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
  LspJitChannel,
  LspOrder,
  orderStateVariant,
} from "src/screens/lsp/types";
import { useFormatLoki } from "src/screens/lsp/useFormatLoki";
import { cn } from "src/lib/utils";
import { shortenMiddle } from "src/utils/nostr";

// The states the filter chips expose, in lifecycle order. PAID is a transient
// state folded into the "All" count (it has no dedicated chip in the design);
// everything else gets a chip that doubles as its count.
const STATE_CHIPS = ["CREATED", "COMPLETED", "FAILED", "EXPIRED"] as const;
type SortKey = "newest" | "amount" | "fee";

export function LspOrders() {
  const { t } = useTranslation("lsp");
  // Segmented sub-tabs within the (layout-provided) Orders page: the orders
  // ledger and the JIT channels those orders funded.
  return (
    <Tabs defaultValue="orders" className="space-y-4">
      <TabsList>
        <TabsTrigger value="orders">{t("orders.tabs.orders")}</TabsTrigger>
        <TabsTrigger value="jit">{t("orders.tabs.jit")}</TabsTrigger>
      </TabsList>
      <TabsContent value="orders">
        <OrdersTab />
      </TabsContent>
      <TabsContent value="jit">
        <JitTab />
      </TabsContent>
    </Tabs>
  );
}

// A state filter chip that also shows its count — the list header is the
// breakdown AND the control, matching the Cash / Circle list headers.
function StateChip({
  label,
  count,
  selected,
  onClick,
}: {
  label: string;
  count: number;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded-full border px-3 py-1.5 text-sm font-medium transition-colors",
        selected
          ? "border-primary/40 bg-primary/15 text-foreground"
          : "border-border bg-card text-muted-foreground hover:text-foreground"
      )}
    >
      {label}
      <span className="ms-1.5 text-xs text-muted-foreground slashed-zero tabular-nums">
        {count}
      </span>
    </button>
  );
}

function OrdersTab() {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  const { data: orders, error } = useLspOrders();
  const [query, setQuery] = React.useState("");
  const [state, setState] = React.useState<string>("ALL");
  const [sort, setSort] = React.useState<SortKey>("newest");
  const navigate = useNavigate();

  // Typed state → label: i18n keys are strongly typed, so a template key
  // `orders.states.${x}` won't compile — map each known state to its literal
  // key and fall back to the raw state for anything unexpected.
  const stateLabel = (s: string): string => {
    switch (s) {
      case "ALL":
        return t("orders.states.all");
      case "CREATED":
        return t("orders.states.created");
      case "PAID":
        return t("orders.states.paid");
      case "COMPLETED":
        return t("orders.states.completed");
      case "FAILED":
        return t("orders.states.failed");
      case "EXPIRED":
        return t("orders.states.expired");
      default:
        return s;
    }
  };

  if (error) {
    return <LspUnavailable error={error} />;
  }
  if (!orders) {
    return <Skeleton className="h-48 w-full" />;
  }

  const counts: Record<string, number> = { ALL: orders.length };
  for (const o of orders) {
    counts[o.state] = (counts[o.state] ?? 0) + 1;
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

  const sorted = [...filtered].sort((a, b) => {
    if (sort === "amount") {
      return b.amount - a.amount;
    }
    if (sort === "fee") {
      return b.fee - a.fee;
    }
    return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
  });

  return (
    <div className="space-y-3">
      {/* Stat-strip header: state chips (with counts) + search + sort. */}
      <div className="flex flex-wrap items-center gap-2">
        <StateChip
          label={stateLabel("ALL")}
          count={counts.ALL ?? 0}
          selected={state === "ALL"}
          onClick={() => setState("ALL")}
        />
        {STATE_CHIPS.map((s) => (
          <StateChip
            key={s}
            label={stateLabel(s)}
            count={counts[s] ?? 0}
            selected={state === s}
            onClick={() => setState(s)}
          />
        ))}
        <div className="relative flex min-w-[12rem] flex-1 items-center">
          <SearchIcon className="text-muted-foreground pointer-events-none absolute start-3 size-4" />
          <Input
            placeholder={t("orders.search")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="ps-9"
          />
        </div>
        <Select value={sort} onValueChange={(v) => setSort(v as SortKey)}>
          <SelectTrigger className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="newest">{t("orders.sort.newest")}</SelectItem>
            <SelectItem value="amount">{t("orders.sort.amount")}</SelectItem>
            <SelectItem value="fee">{t("orders.sort.fee")}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {sorted.length === 0 ? (
        <p className="text-muted-foreground py-8 text-center text-sm">
          {t("orders.empty")}
        </p>
      ) : (
        <>
          <div className="overflow-hidden rounded-xl border">
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
                  <TableHead className="text-right">
                    {t("orders.table.created")}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sorted.map((o: LspOrder) => (
                  <TableRow
                    key={o.order_id}
                    className="cursor-pointer"
                    onClick={() =>
                      navigate(`/lsp/orders/${encodeURIComponent(o.order_id)}`)
                    }
                  >
                    <TableCell className="font-mono text-xs">
                      {shortenMiddle(o.order_id)}
                    </TableCell>
                    <TableCell className="max-w-[12rem] truncate font-mono text-xs">
                      {o.peer_alias || shortenMiddle(o.peer_id)}
                    </TableCell>
                    <TableCell>
                      <Badge variant={orderStateVariant(o.state)}>
                        {stateLabel(o.state)}
                      </Badge>
                    </TableCell>
                    <TableCell className="slashed-zero text-right tabular-nums">
                      {formatLoki(o.amount)}
                    </TableCell>
                    <TableCell className="slashed-zero text-right tabular-nums">
                      {formatLoki(o.fee)}
                    </TableCell>
                    <TableCell className="text-muted-foreground slashed-zero text-right text-xs tabular-nums">
                      {new Date(o.created_at).toLocaleString()}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <p className="text-muted-foreground text-sm slashed-zero">
            {t("orders.showingHint", {
              filtered: sorted.length,
              total: orders.length,
            })}
          </p>
        </>
      )}
    </div>
  );
}

function JitTab() {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  const { data: channels, error } = useLspJitChannels();
  if (error) {
    return <LspUnavailable error={error} />;
  }
  if (!channels) {
    return <Skeleton className="h-48 w-full" />;
  }
  if (channels.length === 0) {
    return (
      <p className="text-muted-foreground py-8 text-center text-sm">
        {t("orders.jit.empty")}
      </p>
    );
  }
  return (
    <div className="overflow-hidden rounded-xl border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("orders.table.order")}</TableHead>
            <TableHead>{t("orders.jit.scid")}</TableHead>
            <TableHead>{t("orders.table.peer")}</TableHead>
            <TableHead className="text-right">
              {t("orders.jit.capacity")}
            </TableHead>
            <TableHead className="text-right">{t("orders.jit.fee")}</TableHead>
            <TableHead className="text-right">
              {t("orders.jit.created")}
            </TableHead>
            <TableHead>{t("orders.jit.status")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {channels.map((c: LspJitChannel) => (
            <TableRow key={c.order_id}>
              <TableCell className="font-mono text-xs">
                {shortenMiddle(c.order_id)}
              </TableCell>
              <TableCell className="font-mono text-xs">
                {c.channel_id || "—"}
              </TableCell>
              <TableCell className="max-w-[12rem] truncate font-mono text-xs">
                {shortenMiddle(c.peer_id)}
              </TableCell>
              <TableCell className="slashed-zero text-right tabular-nums">
                {formatLoki(c.capacity)}
              </TableCell>
              <TableCell className="slashed-zero text-right tabular-nums">
                {formatLoki(c.fee)}
              </TableCell>
              <TableCell className="text-muted-foreground slashed-zero text-right text-xs tabular-nums">
                {c.created_at ? new Date(c.created_at).toLocaleString() : "—"}
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
    </div>
  );
}
