// Accent-colour override. The Settings picker sets the theme's accent tokens
// (`--primary` and the states that follow it) as an inline override on <html>,
// so every component that reads those tokens re-tints without any hardcoded
// colour. "default" removes the override and falls back to whatever the active
// theme defines. HSL triplets (not hex) keep them in the same shape as the
// theme files; they are the one legitimate colour source here — the accent the
// user is choosing.
//
// Shared between the Settings picker (where it's chosen) and ThemeProvider
// (which re-asserts it on every theme/dark-mode change and on cold start), so
// the override survives a page reload, not only a visit to Settings.
export const ACCENT_KEY = "lokihub-accent";

export const ACCENT_TOKENS = [
  "--primary",
  "--ring",
  "--sidebar-primary",
  "--sidebar-ring",
] as const;

export const ACCENTS: { id: string; value: string | null }[] = [
  { id: "default", value: null },
  { id: "amber", value: "37 72% 49%" },
  { id: "green", value: "150 52% 40%" },
  { id: "teal", value: "176 58% 37%" },
  { id: "blue", value: "212 70% 50%" },
  { id: "purple", value: "265 52% 56%" },
  { id: "rose", value: "349 64% 54%" },
];

// Apply an accent by its HSL triplet (or null to clear the override).
export function applyAccent(value: string | null) {
  const root = document.documentElement;
  if (!value) {
    ACCENT_TOKENS.forEach((p) => root.style.removeProperty(p));
  } else {
    ACCENT_TOKENS.forEach((p) => root.style.setProperty(p, `hsl(${value})`));
  }
}

// Re-assert the accent stored in localStorage onto the four tokens. The theme
// class change wipes inline overrides, so ThemeProvider calls this after every
// theme/dark-mode switch and on first mount.
export function reapplyStoredAccent() {
  const id = localStorage.getItem(ACCENT_KEY) || "default";
  applyAccent(ACCENTS.find((a) => a.id === id)?.value ?? null);
}
