// Minimal BIP-173 bech32 decoder — no encoder, since this codebase only
// ever needs to decode a bech32 string another nmilat-family tool already
// produced (today: nconnection1..., NIP-IC's ConnectionKey envelope), never
// mint one client-side. Deliberately self-contained rather than pulling in
// a bech32 npm package: the algorithm is small and stable, and this mirrors
// the Go side's own small, self-contained
// github.com/flokiorg/go-flokicoin/chainutil/bech32 package rather than
// adding a dependency for ~60 lines of well-known code.
//
// Standard bech32 (BIP-173), not bech32m (BIP-350) — the same variant NIP-19
// uses for npub/nprofile/etc., and the one nconnection1... documents itself
// as following (nipIC/nconnection.go: "the same pattern NIP-19 uses for
// nprofile/nevent").
const CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";
const GENERATOR = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];

function polymod(values: number[]): number {
  let chk = 1;
  for (const v of values) {
    const top = chk >>> 25;
    chk = ((chk & 0x1ffffff) << 5) ^ v;
    for (let i = 0; i < 5; i++) {
      if ((top >>> i) & 1) {
        chk ^= GENERATOR[i];
      }
    }
  }
  return chk;
}

function hrpExpand(hrp: string): number[] {
  const ret: number[] = [];
  for (let i = 0; i < hrp.length; i++) {
    ret.push(hrp.charCodeAt(i) >> 5);
  }
  ret.push(0);
  for (let i = 0; i < hrp.length; i++) {
    ret.push(hrp.charCodeAt(i) & 31);
  }
  return ret;
}

function verifyChecksum(hrp: string, data: number[]): boolean {
  return polymod(hrpExpand(hrp).concat(data)) === 1;
}

// bech32Decode returns the human-readable prefix and the 5-bit data words
// (checksum already stripped), or undefined for anything malformed —
// wrong/mixed case, out-of-range characters, bad checksum, etc. No
// BIP-173 90-character total-length cap is enforced: nconnection1... (like
// nprofile1... before it) routinely exceeds it once it carries relay hints,
// and its own encoder never enforced the cap either.
export function bech32Decode(
  input: string
): { prefix: string; words: number[] } | undefined {
  if (input.length < 8) {
    return undefined;
  }
  const hasLower = /[a-z]/.test(input);
  const hasUpper = /[A-Z]/.test(input);
  if (hasLower && hasUpper) {
    return undefined;
  }
  for (let i = 0; i < input.length; i++) {
    const c = input.charCodeAt(i);
    if (c < 33 || c > 126) {
      return undefined;
    }
  }
  const lowered = input.toLowerCase();
  const pos = lowered.lastIndexOf("1");
  if (pos < 1 || pos + 7 > lowered.length) {
    return undefined;
  }
  const hrp = lowered.slice(0, pos);
  const dataChars = lowered.slice(pos + 1);
  const data: number[] = [];
  for (const ch of dataChars) {
    const idx = CHARSET.indexOf(ch);
    if (idx === -1) {
      return undefined;
    }
    data.push(idx);
  }
  if (!verifyChecksum(hrp, data)) {
    return undefined;
  }
  return { prefix: hrp, words: data.slice(0, -6) };
}

// convertBits regroups an array of fromBits-wide values into toBits-wide
// values — the 5-bit bech32 data part <-> 8-bit raw TLV bytes conversion
// every NIP-19-style envelope (nprofile, nconnection, ...) needs. Returns
// undefined on malformed input (a value too wide for fromBits, or leftover
// bits that don't cleanly pad when pad is false) rather than throwing.
export function convertBits(
  data: number[] | Uint8Array,
  fromBits: number,
  toBits: number,
  pad: boolean
): number[] | undefined {
  let acc = 0;
  let bits = 0;
  const ret: number[] = [];
  const maxv = (1 << toBits) - 1;
  const maxAcc = (1 << (fromBits + toBits - 1)) - 1;
  for (const value of data) {
    if (value < 0 || value >> fromBits) {
      return undefined;
    }
    acc = ((acc << fromBits) | value) & maxAcc;
    bits += fromBits;
    while (bits >= toBits) {
      bits -= toBits;
      ret.push((acc >> bits) & maxv);
    }
  }
  if (pad) {
    if (bits > 0) {
      ret.push((acc << (toBits - bits)) & maxv);
    }
  } else if (bits >= fromBits || (acc << (toBits - bits)) & maxv) {
    return undefined;
  }
  return ret;
}
