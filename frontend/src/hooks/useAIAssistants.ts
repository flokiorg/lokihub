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
  createdAt: string;
};

export type AIActivity = {
  time: string;
  appId: number;
  tool: string;
  outcome:
    | "done"
    | "failed"
    | "needs_confirmation"
    | "pending_approval"
    | "executing"
    | "denied"
    | "expired";
  detail?: string;
};

export type AIControls = {
  paused?: boolean;
  /** true only in the desktop app, which can serve assistants on this computer */
  loopbackSupported?: boolean;
  /** port of the local listener on 127.0.0.1 (0 = off) */
  loopbackPort?: number;
  /** payments/transfers above this many loki wait for the owner's approval */
  approvalLimitLoki?: number;
};

export type CreatedAssistant = {
  appId: number;
  appPubkey: string;
  token: string;
};

export function useAIAssistants() {
  const [assistants, setAssistants] = useState<AIAssistant[]>([]);
  const [pending, setPending] = useState<AIPendingAction[]>([]);
  const [controls, setControls] = useState<AIControls>({});
  const [activity, setActivity] = useState<AIActivity[]>([]);
  const [loading, setLoading] = useState(true);

  const reload = useCallback(async () => {
    try {
      const [a, p, c, act] = await Promise.all([
        request<AIAssistant[]>("/api/mcp/connections"),
        request<AIPendingAction[]>("/api/mcp/pending"),
        request<AIControls>("/api/mcp/controls"),
        request<AIActivity[]>("/api/mcp/activity?limit=50"),
      ]);
      setAssistants(a ?? []);
      setPending(p ?? []);
      setControls(c ?? {});
      setActivity(act ?? []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    reload();
    // Keep the approval queue live while the page is open: an assistant's
    // request should appear without a manual refresh.
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") {
        reload();
      }
    }, 10_000);
    return () => clearInterval(timer);
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
    activity,
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
      "Can send payments from your whole balance, create invoices, move funds between your main wallet and sub-wallets (cash hubs, circles, isolated apps) and sign messages with your node key. Each payment needs the assistant to show a preview and confirm it — the hub can't check that you agreed — and once your assistants' payments in the last 24 hours reach your approval limit, the rest wait for you here. Every on-chain send waits for your approval.",
  },
  {
    id: "cash_circles",
    title: "Cash & circles",
    read: "See cash hubs, cash bills, circles and members.",
    writeRisk:
      "Can create and reclaim cash, change what customers pay (order terms), change circle members and follow sets, delete cash wallets or whole circles, and create public selling connections for your cash hubs. Each of these needs the assistant to show a preview and confirm it; creating cash and changing order terms also wait for your approval here.",
  },
  {
    id: "channels",
    title: "Network & liquidity",
    read: "See channels, peers and liquidity.",
    writeRisk:
      "Can connect peers, change channel fees, add liquidity providers (LSPs) and — after your approval here — trust an LSP and buy channels from it, which costs fees. Opening or closing a channel always needs your approval.",
    advanced: true,
  },
  {
    id: "listings",
    title: "Listings & discoverability",
    read: "See your listings and where they are published.",
    writeRisk:
      "Can create, edit, pause, unlist, reopen and republish your listings and change the relays they are published to — publicly, under your node's identity. Each change needs the assistant to show a preview and confirm it.",
  },
  {
    id: "node",
    title: "Node info & hub settings",
    read: "See node status and hub info.",
    writeRisk:
      "Can rename the node, change display settings, and publish public notes and your node profile under your node's identity (the profile can carry a payment address, so it waits for your approval here).",
    advanced: true,
  },
  {
    id: "connections",
    title: "App connections",
    read: "See your connected apps and identity authorities.",
    writeRisk:
      "Can delete connected apps — including cash hubs and circles, whose money then goes back to your hub — but never your other AI assistants, and can add or remove identity authorities. Each deletion needs the assistant to show a preview and confirm it. It can never create wallet connections.",
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

/** grantsToLevels is the inverse of levelsToGrants (unset groups are "off"). */
export function grantsToLevels(grants: AIGrants): AssistantLevels {
  const levels = { ...DEFAULT_ASSISTANT_LEVELS };
  for (const g of AI_GROUPS) {
    levels[g.id] = grants[g.id] ?? "off";
  }
  return levels;
}

/**
 * editAssistant renames an assistant, changes what it may do, and/or rotates
 * its key. Returns the new key when rotated (shown once).
 */
export async function editAssistant(
  appId: number,
  change: { name?: string; groups?: AIGrants; rotateKey?: boolean }
): Promise<string | undefined> {
  const res = await request<{ token?: string }>(
    `/api/mcp/connections/${appId}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(change),
    }
  );
  return res?.token || undefined;
}
