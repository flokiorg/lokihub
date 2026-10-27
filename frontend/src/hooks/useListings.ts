import useSWR from "swr";

import { swrFetcher } from "src/utils/swr";
import { request } from "src/utils/request";

// A listing as GET /api/listings shows it (WP-25). lokihub never holds the
// operator's key: the operator line is read-only.
export type Listing = {
  id: number;
  type: "cash_order" | "circle_wallet";
  hubAppId: number;
  connAppId: number;
  d: string;
  address: string;
  addressRelay: string;
  aTag: string[];
  operatorCommand: string;
  title: string;
  content: string;
  image: string;
  location: string;
  languages: string[] | null;
  geohashes: string[] | null;
  hints: boolean;
  followSetA: boolean;
  operatorPubkey: string;
  operatorRelay: string;
  status: "open" | "paused" | "closed";
  phase: "listed" | "closing" | "deleted";
  connection: string;
  lastCreatedAt: number;
  lastEventId: string;
  lastOpenExpiresAt?: number | null;
  relaysOk: number;
  relaysTotal: number;
  failing: boolean;
  operatorVerified?: boolean | null;
  operatorCheckedAt?: number | null;
  closedAt?: number | null;
  deleteDueAt?: number | null;
  deletedAt?: number | null;
};

// The fields the operator enters (ListOfferDialog).
export type ListingFields = {
  title: string;
  content: string;
  image: string;
  location: string;
  languages: string[];
  geohashes: string[];
  hints: boolean;
  follow_set_a: boolean;
  confirm_follow_set_public: boolean;
  operator_pubkey: string;
  operator_relay: string;
};

export type ListingAction =
  | "pause"
  | "resume"
  | "unlist"
  | "revoke-connection"
  | "check-operator";

export function useListings() {
  return useSWR<Listing[]>("/api/listings", swrFetcher);
}

export function useListingRelays() {
  return useSWR<{ configured: string[]; effective: string[] }>(
    "/api/listing-relays",
    swrFetcher
  );
}

export function createListing(
  body: { circle_hub_app_id?: number; conn_app_id?: number } & ListingFields
) {
  return request<Listing>("/api/listings", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export function updateListing(id: number, body: ListingFields) {
  return request<Listing>(`/api/listings/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export function listingAction(id: number, action: ListingAction) {
  return request<Listing>(`/api/listings/${id}/${action}`, { method: "POST" });
}

export function setListingRelays(relays: string[]) {
  return request<{ configured: string[]; effective: string[] }>(
    "/api/listing-relays",
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ relays }),
    }
  );
}

export function regenerateCashHubConnection(hubId: number) {
  return request<{ cashHubToken: string }>(
    `/api/cash-hubs/${hubId}/revoke-connection`,
    { method: "POST" }
  );
}
