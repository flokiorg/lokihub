// Shared chart vocabulary: colours, axis styling and unit conversion.
//
// Kept out of any component module on purpose. A file that exports both
// components and constants breaks React fast refresh (react-refresh/
// only-export-components), and these are needed by both the cash and circle
// charts.

// Categorical series colours, from the tokens defined in themes/index.css.
//
// NOT --chart-1..5: those are a sequential ramp — in claymorphism every stop
// shares hue ~277 and differs only in lightness, and default reuses gold at
// both ends — which is the right encoding for ordered magnitude and the wrong
// one for telling independent series apart.
//
// These carry meaning rather than order, and hold it across every chart:
// green is value that is safely held, amber is value owed or committed, red
// is value lost, blue is value that came back, grey is movement that stayed
// inside the system.
export const SERIES = {
  covered: "var(--color-series-covered)",
  owed: "var(--color-series-owed)",
  urgent: "var(--color-series-urgent)",
  info: "var(--color-series-info)",
  muted: "var(--color-series-muted)",
};

export const axisProps = {
  stroke: "var(--muted-foreground)",
  fontSize: 11,
  tickLine: false,
  axisLine: false,
};

// Charts are drawn in loki, not mloki: an axis labelled in thousandths is
// unreadable, and the figures above every chart already carry exact amounts.
export const toLoki = (mloki: number) => Math.floor(mloki / 1000);

// "2026-09-23" -> "09-23". The year is constant across a 30-day window, so on
// an axis it is noise.
export const shortDate = (date: string) => date.slice(5);

export type TooltipRow = {
  name: string;
  color: string;
  mloki: number;
  count?: number;
};
