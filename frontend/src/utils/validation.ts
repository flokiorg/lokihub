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
export const validateNwc = (url: string): boolean => {
  if (!url) return true;
  try {
    nip47.parseConnectionString(url);
    return true;
  } catch (e) {
    toast.error(
      "Invalid Messageboard NWC URL. Must be valid nostr+walletconnect://"
    );
    return false;
  }
};
