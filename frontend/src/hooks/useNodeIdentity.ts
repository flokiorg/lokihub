import { useMemo } from "react";

import { useInfo } from "src/hooks/useInfo";
import { useNodeConnectionInfo } from "src/hooks/useNodeConnectionInfo";
import { splitRelayUrls } from "src/hooks/useNdk";
import { safeNpubEncode, safeNprofileEncode } from "src/utils/nostr";

export interface NodeIdentityFormats {
  // Raw LN node pubkey, compressed (66 hex chars, 02/03-prefixed) — what
  // /api/node/connection-info and the Channels "Advanced" menu already show.
  hex?: string;
  // The same identity in Nostr pubkey format: hex with the leading
  // 02/03 prefix byte dropped, matching service/private_transport.go's
  // newPrivateTransport (`compressed[2:]`) exactly — this is the identity a
  // cashctl client recovers from a bill's mint signature and checks a hub's
  // private-transport announcement against.
  nostrHex?: string;
  npub?: string;
  // nprofile hints are the hub's own General relays (info.generalRelay),
  // not the NWC relay — General is where a profile/contact lookup for this
  // identity would actually be served from.
  nprofile?: string;
}

// useNodeIdentity derives the hub's LN/Nostr identity in every format a
// human or client needs to compare it against: hex, the private-transport
// Nostr pubkey, npub, and nprofile. Entirely client-side — no new backend
// endpoint, since /api/node/connection-info already has the compressed hex
// pubkey and /api/info already has the General relay list.
export function useNodeIdentity(): NodeIdentityFormats {
  const { data: nodeConnectionInfo } = useNodeConnectionInfo();
  const { data: info } = useInfo();

  const generalRelays = useMemo(
    () => splitRelayUrls(info?.generalRelay),
    [info]
  );

  return useMemo(() => {
    const hex = nodeConnectionInfo?.pubkey;
    if (!hex || hex.length !== 66) {
      return {};
    }
    const nostrHex = hex.slice(2);
    return {
      hex,
      nostrHex,
      npub: safeNpubEncode(nostrHex),
      nprofile: safeNprofileEncode(nostrHex, generalRelays),
    };
  }, [nodeConnectionInfo, generalRelays]);
}
