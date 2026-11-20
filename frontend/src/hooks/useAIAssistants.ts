import { useCallback, useEffect, useState } from "react";
import { request } from "src/utils/request";

// Owner API for AI-assistant (MCP) connections — see lokihub http/mcp_*.go.

export type AIGroup =
  | "wallet"
  | "cash_circles"
  | "channels"
  | "listings"
  | "node"
  | "connections";
export type AIMode = "read" | "write";
export type AIGrants = Partial<Record<AIGroup, AIMode>>;

export type AIAssistant = {
  appId: number;
  appPubkey: string;
  name: string;
  groups: AIGrants;
  createdAt: string;
  lastUsedAt?: string;
  expiresAt?: string;
};

export type AIPendingAction = {
  id: string;
  appId: number;
  tool: string;
  summary: string;
  status: "open" | "approved" | "denied";
};

export type AIControls = { paused?: boolean };

export type CreatedAssistant = {
  appId: number;
  appPubkey: string;
  token: string;
};

export function useAIAssistants() {
  const [assistants, setAssistants] = useState<AIAssistant[]>([]);
  const [pending, setPending] = useState<AIPendingAction[]>([]);
  const [controls, setControls] = useState<AIControls>({});
  const [loading, setLoading] = useState(true);

  const reload = useCallback(async () => {
    try {
      const [a, p, c] = await Promise.all([
        request<AIAssistant[]>("/api/mcp/connections"),
        request<AIPendingAction[]>("/api/mcp/pending"),
        request<AIControls>("/api/mcp/controls"),
      ]);
      setAssistants(a ?? []);
      setPending(p ?? []);
      setControls(c ?? {});
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);

  const revoke = useCallback(
    async (appPubkey: string) => {
      await request(`/api/apps/${appPubkey}`, { method: "DELETE" });
      await reload();
    },
    [reload]
  );

  const decide = useCallback(
    async (id: string, decision: "approve" | "deny") => {
      await request(`/api/mcp/pending/${encodeURIComponent(id)}/${decision}`, {
        method: "POST",
      });
      await reload();
    },
    [reload]
  );

  const updateControls = useCallback(
    async (next: AIControls) => {
      await request("/api/mcp/controls", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(next),
      });
      await reload();
    },
    [reload]
  );

  return {
    assistants,
    pending: pending.filter((p) => p.status === "open"),
    controls,
    loading,
    reload,
    revoke,
    decide,
    updateControls,
  };
}

export async function createAssistant(
  name: string,
  groups: AIGrants,
  expiresInDays: number
): Promise<CreatedAssistant | undefined> {
  return request<CreatedAssistant>("/api/mcp/connections", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, groups, expiresInDays }),
  });
}

// Display metadata for the six groups, in the shipped redesign vocabulary
// (not the reverted Money/Network grouping). writeRisk is shown when the owner
// grants Read+Write, so destructive power is never hidden behind a toggle.
export const AI_GROUPS: {
  id: AIGroup;
  title: string;
  read: string;
  writeRisk: string;
  advanced?: boolean;
}[] = [
  {
    id: "wallet",
    title: "Wallet & payments",
    read: "See balances and transactions.",
    writeRisk:
      "Can send payments from your whole balance, create invoices, and move funds between your main wallet and sub-wallets (cash hubs, circles, isolated apps). Every payment is previewed with a warning first and only goes through on a second, confirmed request. On-chain sends to a new address still need your approval.",
  },
  {
    id: "cash_circles",
    title: "Cash & circles",
    read: "See cash hubs, cash bills, circles and members.",
    writeRisk:
      "Can mint and reclaim cash, change circle members, delete cash wallets or whole circles, and create public selling connections for your cash hubs. Anything that takes back or settles money is previewed with a warning first and needs a second, confirmed request.",
  },
  {
    id: "channels",
    title: "Network & liquidity",
    read: "See channels, peers and liquidity.",
    writeRisk:
      "Can connect peers and change channel fees. Opening or closing a channel still needs your approval.",
    advanced: true,
  },
  {
    id: "listings",
    title: "Listings & discoverability",
    read: "See your listings and where they are published.",
    writeRisk: "Can change where your listings are published.",
  },
  {
    id: "node",
    title: "Node info & hub settings",
    read: "See node status and hub info.",
    writeRisk: "Can rename the node and change display settings.",
    advanced: true,
  },
  {
    id: "connections",
    title: "App connections",
    read: "See your connected apps and identity authorities.",
    writeRisk:
      "Can revoke connected apps (not your other AI assistants) and manage identity authorities. It can never create wallet connections.",
  },
];

export type AssistantLevel = "off" | "read" | "write";
export type AssistantLevels = Record<AIGroup, AssistantLevel>;

export const DEFAULT_ASSISTANT_LEVELS: AssistantLevels = {
  wallet: "read",
  cash_circles: "off",
  channels: "off",
  listings: "off",
  node: "read",
  connections: "off",
};

export function levelsToGrants(levels: AssistantLevels): AIGrants {
  const g: AIGrants = {};
  for (const [id, level] of Object.entries(levels) as [
    AIGroup,
    AssistantLevel,
  ][]) {
    if (level !== "off") {
      g[id] = level;
    }
  }
  return g;
}
