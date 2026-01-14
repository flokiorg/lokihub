import { nip47 } from "nostr-tools";
import { toast } from "sonner";

/**
 * Validates a URL against allowed protocols
 * @param url - URL to validate
 * @param name - Display name for error messages
 * @param protocols - Allowed protocols (default: ["https"])
 * @returns true if valid, false otherwise (shows toast on error)
 */
export const validateUrl = (
  url: string,
  name: string,
  protocols: string[] = ["https"]
): boolean => {
  if (!url) return true;

  const isValidProtocol = protocols.some((protocol) =>
    url.startsWith(`${protocol}://`)
  );
  if (!isValidProtocol) {
    toast.error(
      `${name} must start with ${protocols.map((p) => p + "://").join(" or ")}`
    );
    return false;
  }
  return true;
};

/**
 * Validates a Nostr Wallet Connect (NWC) URL
 * @param url - NWC URL to validate
 * @returns true if valid, false otherwise (shows toast on error)
 */
export const validateWebSocketURL = (
  url: string,
  name: string
): string | null => {
  if (!url) return null;
  if (!url.startsWith("wss://") && !url.startsWith("ws://")) {
    return `${name} must start with wss:// or ws://`;
  }
  return null;
};

export const validateHTTPURL = (
  url: string,
  name: string
): string | null => {
  if (!url) return null;
  if (!url.startsWith("https://") && !url.startsWith("http://")) {
    return `${name} must start with https:// or http://`;
  }
  return null;
};

export const validateMessageBoardURL = (url: string): string | null => {
  if (!url) return null;
  if (!url.startsWith("nostr+walletconnect://")) {
    return "Messageboard NWC URL must start with nostr+walletconnect://";
  }
  try {
    nip47.parseConnectionString(url);
    return null;
  } catch (e) {
    return "Invalid Messageboard NWC URL. Must be valid nostr+walletconnect://";
  }
};

/**
 * @deprecated Use validateMessageBoardURL instead
 */
export const validateNwc = (url: string): boolean => {
  const error = validateMessageBoardURL(url);
  if (error) {
    toast.error(error);
    return false;
  }
  return true;
};

export const validateLSPURI = (uri: string): string | null => {
  if (!uri) return "URI is required";
  const parts = uri.split("@");
  if (parts.length !== 2) return "Invalid format. Expected pubkey@host:port";
  
  const pubkey = parts[0];
  const host = parts[1];

  if (!/^[0-9a-fA-F]{66}$/.test(pubkey)) {
    return "Invalid pubkey. Must be 33-byte hex string";
  }
  if (!host) {
     return "Host is required";
  }
  return null;
};
