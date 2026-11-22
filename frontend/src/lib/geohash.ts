// Minimal geohash encode/decode — enough to turn a map tap into the coarse
// 3–4 character region prefix NIP-CASH listings use, and back into a cell to
// draw. No dependency; the algorithm is the standard base-32 geohash.

const BASE32 = "0123456789bcdefghjkmnpqrstuvwxyz";

// encodeGeohash returns the geohash prefix of `precision` characters for a
// latitude/longitude. precision 3 ≈ 150 km cells, 4 ≈ 40 km.
export function encodeGeohash(
  lat: number,
  lng: number,
  precision: number
): string {
  let idx = 0;
  let bit = 0;
  let evenBit = true;
  let geohash = "";
  let latMin = -90;
  let latMax = 90;
  let lngMin = -180;
  let lngMax = 180;

  while (geohash.length < precision) {
    if (evenBit) {
      const mid = (lngMin + lngMax) / 2;
      if (lng >= mid) {
        idx = idx * 2 + 1;
        lngMin = mid;
      } else {
        idx = idx * 2;
        lngMax = mid;
      }
    } else {
      const mid = (latMin + latMax) / 2;
      if (lat >= mid) {
        idx = idx * 2 + 1;
        latMin = mid;
      } else {
        idx = idx * 2;
        latMax = mid;
      }
    }
    evenBit = !evenBit;
    if (++bit === 5) {
      geohash += BASE32[idx];
      bit = 0;
      idx = 0;
    }
  }
  return geohash;
}

export type GeoBounds = {
  latMin: number;
  latMax: number;
  lngMin: number;
  lngMax: number;
};

// geohashBounds returns the lat/lng box a geohash prefix covers, for drawing
// the selected cell on the map. Returns null for an invalid character.
export function geohashBounds(geohash: string): GeoBounds | null {
  let evenBit = true;
  let latMin = -90;
  let latMax = 90;
  let lngMin = -180;
  let lngMax = 180;

  for (const ch of geohash.toLowerCase()) {
    const cd = BASE32.indexOf(ch);
    if (cd === -1) {
      return null;
    }
    for (let n = 4; n >= 0; n--) {
      const bitN = (cd >> n) & 1;
      if (evenBit) {
        const mid = (lngMin + lngMax) / 2;
        if (bitN === 1) {
          lngMin = mid;
        } else {
          lngMax = mid;
        }
      } else {
        const mid = (latMin + latMax) / 2;
        if (bitN === 1) {
          latMin = mid;
        } else {
          latMax = mid;
        }
      }
      evenBit = !evenBit;
    }
  }
  return { latMin, latMax, lngMin, lngMax };
}
