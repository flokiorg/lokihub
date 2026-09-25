import NDK, { NDKRelaySet } from "@nostr-dev-kit/ndk";

// Bounds how many extra outbox relays get merged in on top of the configured
// base relays — NDKRelaySet.fromRelayUrls eagerly connects to every relay it
// doesn't already know about, so an uncapped union (e.g. across an entire
// following list) could open dozens of concurrent WebSocket connections.
const MAX_OUTBOX_RELAYS = 6;
// Ceiling on the unioned set for a multi-author fetch. Generous on purpose:
// it exists to stop a page with very many authors opening an unbounded number
// of connections, not to ration authors against each other — rationing them
// is what made later recipients resolve to nothing.
const MAX_OUTBOX_TOTAL_RELAYS = 24;

function dedupe(urls: string[]): string[] {
  return Array.from(new Set(urls));
}

// Picks the outbox relays for a multi-author fetch: capped PER AUTHOR, then
// unioned — not unioned then capped.
//
// Flattening first and slicing the result meant the first author whose relays
// happened to come back filled the whole budget, and every author after them
// contributed nothing: on a page listing many recipients the later ones
// silently resolved to no profile at all. Each author gets their own
// allowance, which is what an outbox model is for.
//
// MAX_OUTBOX_TOTAL_RELAYS is a separate ceiling so a page with very many
// authors cannot open an unbounded number of connections. It sits far above
// the per-author cap on purpose, biting only in the extreme case rather than
// in the ordinary one the old code broke.
//
// Exported for its own tests: the bug was entirely in this arithmetic, and
// reaching it through NDK would mean mocking an outbox tracker to assert
// something that has nothing to do with NDK.
export function selectOutboxUrls(perAuthorRelays: string[][]): string[] {
  return dedupe(
    perAuthorRelays.flatMap((relays) => relays.slice(0, MAX_OUTBOX_RELAYS))
  ).slice(0, MAX_OUTBOX_TOTAL_RELAYS);
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
  const outboxUrls = selectOutboxUrls(
    pubkeys.map((pubkey) =>
      Array.from(ndk.outboxTracker?.data.get(pubkey)?.writeRelays ?? [])
    )
  );
  return NDKRelaySet.fromRelayUrls(
    dedupe([...baseRelayUrls, ...outboxUrls, ...directiveRelayUrls]),
    ndk
  );
}
