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
    // Outbox model OFF, deliberately. With it on, NDK reads each author's
    // NIP-65 (kind:10002) relay list and opens WebSockets to whatever write
    // relays it finds there — relays the user never selected. That violates
    // the rule that lokihub only ever connects to relays the user picked under
    // Settings > Services. Every socket this app opens must trace back to
    // explicitRelayUrls, which come from /api/info (the user's GeneralRelay /
    // SearchRelay selection). A pubkey's own relay hints are never auto-dialed;
    // lib/nostrRelaySet.ts stays confined to the configured set (its outbox
    // widening is a no-op without an outboxTracker).
    enableOutboxModel: false,
    cacheAdapter: new NDKCacheAdapterDexie({ dbName: "lokihub-ndk-cache" }),
  });
  connectedRelayKey = key;
  ndkInstance.connect();
  return ndkInstance;
}
