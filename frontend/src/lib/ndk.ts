import NDK from "@nostr-dev-kit/ndk";
import NDKCacheAdapterDexie from "@nostr-dev-kit/ndk-cache-dexie";

let ndkInstance: NDK | undefined;
let connectedRelayKey = "";

// getNdk returns a shared NDK instance connected to relayUrls, backed by an
// IndexedDB (dexie) cache so kind:0 profile events persist across reloads.
// Lazy by construction — nothing connects until a caller actually needs it
// (a Circles card rendering a profile), so pages with no circles pay zero
// NDK/WebSocket/IndexedDB cost. Re-creates only if the relay set itself
// changes, which in practice is effectively static at runtime.
export function getNdk(relayUrls: string[]): NDK {
  const key = relayUrls.slice().sort().join(",");
  if (ndkInstance && connectedRelayKey === key) {
    return ndkInstance;
  }
  // Close out the previous instance's relay sockets before replacing it —
  // NDK has no single "destroy" call, so each relay in the pool is
  // disconnected individually to avoid leaking open WebSocket connections
  // when the relay set changes at runtime.
  if (ndkInstance) {
    ndkInstance.pool.relays.forEach((relay) => relay.disconnect());
    ndkInstance.outboxPool?.relays.forEach((relay) => relay.disconnect());
  }
  ndkInstance = new NDK({
    explicitRelayUrls: relayUrls,
    // enableOutboxModel gives every caller a shared, NDK-managed cache of
    // per-pubkey NIP-65 relay lists (ndk.outboxTracker) instead of each
    // hook re-resolving the same pubkey independently. outboxRelayUrls
    // scopes that discovery to our own configured relays instead of NDK's
    // hardcoded external default (purplepag.es/nos.lol) — every relay this
    // app talks to should trace back to Settings, not a library default.
    enableOutboxModel: true,
    outboxRelayUrls: relayUrls,
    cacheAdapter: new NDKCacheAdapterDexie({ dbName: "lokihub-ndk-cache" }),
  });
  connectedRelayKey = key;
  ndkInstance.connect();
  return ndkInstance;
}
