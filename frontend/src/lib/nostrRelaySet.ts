import NDK, { NDKRelaySet } from "@nostr-dev-kit/ndk";

// Bounds how many extra outbox relays get merged in on top of the configured
// base relays — NDKRelaySet.fromRelayUrls eagerly connects to every relay it
// doesn't already know about, so an uncapped union (e.g. across an entire
// following list) could open dozens of concurrent WebSocket connections.
const MAX_OUTBOX_RELAYS = 6;

function dedupe(urls: string[]): string[] {
  return Array.from(new Set(urls));
}

// Merges the configured General relays with a pubkey's own NIP-65 outbox
// (write) relays, so identity lookups aren't limited to the static relay
// list. Outbox discovery goes through ndk.outboxTracker (requires
// enableOutboxModel, see lib/ndk.ts) instead of a standalone lookup, so the
// result is cached once per pubkey and shared across every caller on this
// NDK instance rather than re-resolved independently per hook. `directive`
// relays — e.g. an Identity Authority's own declared relay_urls, or an
// nprofile1...'s embedded relay hints — are unioned in uncapped (unlike
// outbox discovery, this is a small, human-curated list an operator
// explicitly pointed at, not something that scales with a following list).
export async function getRelaySetForPubkey(
  ndk: NDK,
  pubkey: string,
  baseRelayUrls: string[],
  directiveRelayUrls: string[] = []
): Promise<NDKRelaySet> {
  await ndk.outboxTracker?.trackUsers([pubkey]);
  const writeRelays = ndk.outboxTracker?.data.get(pubkey)?.writeRelays;
  const outboxUrls = dedupe(writeRelays ? Array.from(writeRelays) : []).slice(
    0,
    MAX_OUTBOX_RELAYS
  );
  return NDKRelaySet.fromRelayUrls(
    dedupe([...baseRelayUrls, ...outboxUrls, ...directiveRelayUrls]),
    ndk
  );
}

// Batched variant for multi-author fetches — unions every author's outbox
// relays (read from the same shared tracker cache), the base list, and any
// directive relays (see getRelaySetForPubkey) into a single relay set.
export async function getRelaySetForPubkeys(
  ndk: NDK,
  pubkeys: string[],
  baseRelayUrls: string[],
  directiveRelayUrls: string[] = []
): Promise<NDKRelaySet> {
  await ndk.outboxTracker?.trackUsers(pubkeys);
  const outboxUrls = dedupe(
    pubkeys.flatMap((pubkey) =>
      Array.from(ndk.outboxTracker?.data.get(pubkey)?.writeRelays ?? [])
    )
  ).slice(0, MAX_OUTBOX_RELAYS);
  return NDKRelaySet.fromRelayUrls(
    dedupe([...baseRelayUrls, ...outboxUrls, ...directiveRelayUrls]),
    ndk
  );
}
