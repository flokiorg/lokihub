import { XIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import worldOutline from "src/components/listings/world-outline.json";
import { encodeGeohash, geohashBounds } from "src/lib/geohash";
import { cn } from "src/lib/utils";

// An offline world map (no tile server) for picking the coarse region a listing
// should be findable in, instead of typing geohash codes. The outline is a
// simplified land silhouette bundled with the app; a tap is projected back to
// lat/lng and encoded to a 3- or 4-character geohash prefix (NIP-CASH's coarse
// region). Selected cells are drawn on top and listed as removable chips.

const RINGS = (worldOutline as { rings: number[][] }).rings;

// Equirectangular projection into a 360×180 viewBox: x = lng + 180, y = 90 −
// lat. Precomputed once since the outline never changes.
const projX = (lng: number) => lng + 180;
const projY = (lat: number) => 90 - lat;

const LAND_PATHS = RINGS.map((flat) => {
  let d = "";
  for (let i = 0; i < flat.length; i += 2) {
    d +=
      (i === 0 ? "M" : "L") +
      projX(flat[i]).toFixed(1) +
      " " +
      projY(flat[i + 1]).toFixed(1);
  }
  return d + "Z";
});

export function GeoRegionPicker({
  value,
  onChange,
}: {
  value: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useTranslation("listings");
  const svgRef = React.useRef<SVGSVGElement>(null);
  // 3 ≈ 150 km (a region), 4 ≈ 40 km (a city-sized area).
  const [precision, setPrecision] = React.useState(3);

  const addAtEvent = (clientX: number, clientY: number) => {
    const svg = svgRef.current;
    if (!svg) {
      return;
    }
    const rect = svg.getBoundingClientRect();
    const lng = ((clientX - rect.left) / rect.width) * 360 - 180;
    const lat = 90 - ((clientY - rect.top) / rect.height) * 180;
    if (lat < -90 || lat > 90 || lng < -180 || lng > 180) {
      return;
    }
    const gh = encodeGeohash(lat, lng, precision);
    // Drop any existing region that overlaps this one (either is a prefix of
    // the other), so the same place isn't stored twice at two precisions — and
    // so re-tapping an exact cell is a no-op rather than a duplicate.
    const next = value.filter((v) => !v.startsWith(gh) && !gh.startsWith(v));
    onChange([...next, gh]);
  };

  // The land silhouette never changes; build its ~395 <path> vnodes once per
  // mount rather than on every parent keystroke that re-renders this picker.
  const land = React.useMemo(
    () =>
      LAND_PATHS.map((d, i) => (
        <path
          key={i}
          d={d}
          className="fill-foreground/15 stroke-foreground/25"
          strokeWidth="0.15"
        />
      )),
    []
  );

  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">{t("map.hint")}</span>
        {/* Coarser vs finer cell — both are within NIP-CASH's 3–4 char range. */}
        <div className="flex overflow-hidden rounded-md border text-xs">
          {[
            [3, t("map.precisionRegion")],
            [4, t("map.precisionArea")],
          ].map(([p, label]) => (
            <button
              key={p as number}
              type="button"
              onClick={() => setPrecision(p as number)}
              className={cn(
                "px-2.5 py-1 transition-colors",
                precision === p
                  ? "bg-primary/15 font-medium text-primary"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border bg-muted/30">
        <svg
          ref={svgRef}
          viewBox="0 0 360 180"
          className="block h-auto w-full cursor-crosshair select-none"
          onClick={(e) => addAtEvent(e.clientX, e.clientY)}
        >
          {/* Faint equator + prime meridian for orientation. */}
          <line
            x1="0"
            y1="90"
            x2="360"
            y2="90"
            className="stroke-foreground/10"
            strokeWidth="0.3"
          />
          <line
            x1="180"
            y1="0"
            x2="180"
            y2="180"
            className="stroke-foreground/10"
            strokeWidth="0.3"
          />
          {land}
          {value.map((gh) => {
            const b = geohashBounds(gh);
            if (!b) {
              return null;
            }
            // The cell rect shows the real coverage, but a 4-char cell is
            // sub-pixel on a world map — a fixed-size dot at its centre keeps
            // every pick visible regardless of precision.
            return (
              <g key={gh}>
                <rect
                  x={projX(b.lngMin)}
                  y={projY(b.latMax)}
                  width={b.lngMax - b.lngMin}
                  height={b.latMax - b.latMin}
                  className="fill-primary/40 stroke-primary"
                  strokeWidth="0.4"
                />
                <circle
                  cx={projX((b.lngMin + b.lngMax) / 2)}
                  cy={projY((b.latMin + b.latMax) / 2)}
                  r="2"
                  className="fill-primary stroke-background"
                  strokeWidth="0.6"
                />
              </g>
            );
          })}
        </svg>
      </div>

      {value.length > 0 ? (
        <div className="flex flex-wrap gap-2">
          {value.map((gh) => (
            <button
              key={gh}
              type="button"
              onClick={() => onChange(value.filter((g) => g !== gh))}
              className="inline-flex items-center gap-1 rounded-full bg-primary/15 px-2.5 py-1 font-mono text-xs font-medium text-primary"
            >
              {gh}
              <XIcon className="size-3" />
            </button>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("map.none")}</p>
      )}
    </div>
  );
}
