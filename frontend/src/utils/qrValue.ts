// The bech32 credentials NIP-CASH, NIP-CO and NIP-CW define.
const CREDENTIAL_HRPS = ["lokicash", "cashhub", "circlehub", "cashorder"];
const BECH32_DATA = /^[02-9ac-hj-np-z]+$/;

// qrValue is what a QR code shows for value: a bech32 credential in uppercase,
// which fits the code's alphanumeric mode (NIP-CASH §Wire Format, SHOULD);
// anything else unchanged. Copy actions keep the lowercase string.
export function qrValue(value: string): string {
  const sep = value.lastIndexOf("1");
  if (sep <= 0) {
    return value;
  }
  const hrp = value.slice(0, sep);
  if (
    !CREDENTIAL_HRPS.includes(hrp) ||
    !BECH32_DATA.test(value.slice(sep + 1))
  ) {
    return value;
  }
  return value.toUpperCase();
}
