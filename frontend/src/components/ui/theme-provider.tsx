import { createContext, useContext, useEffect, useState } from "react";
import { isHttpMode } from "src/utils/isHttpMode";

export type DarkMode = "system" | "light" | "dark";
export const Themes = [
  "default",

  "nostr",
  "matrix",
  "ghibli",
  "claymorphism",
] as const;
export type Theme = (typeof Themes)[number];

type ThemeProviderProps = {
  children: React.ReactNode;
  defaultTheme?: Theme;
  defaultDarkMode?: DarkMode;
  storageKey?: string;
};

type ThemeProviderState = {
  theme: string;
  darkMode: string;
  setTheme: (theme: Theme) => void;
  setDarkMode: (mode: DarkMode) => void;
  isDarkMode: boolean;
};

const initialState: ThemeProviderState = {
  theme: "default",
  setTheme: () => null,
  darkMode: "system",
  setDarkMode: () => null,
  isDarkMode: false,
};

const ThemeProviderContext = createContext<ThemeProviderState>(initialState);

// Keeps the native window's own background (see BackgroundColour in
// wails/wails_app.go) in sync with whichever theme/appearance is active.
// That colour isn't drawn by our CSS - it's what shows through the native
// macOS window's rounded corners (e.g. behind the inset traffic lights)
// and briefly before the WebView paints - so without this it stays frozen
// on whatever was set at startup even after switching theme or light/dark
// mode. `body` already resolves `bg-background` (themes/index.css), so
// reading its computed style picks up the right colour for every theme
// (including ones added later) without hardcoding a colour table here.
function syncNativeWindowBackground() {
  if (isHttpMode()) {
    return;
  }

  const rgb = getComputedStyle(document.body)
    .backgroundColor.match(/[\d.]+/g)
    ?.map(Number);
  if (!rgb || rgb.length < 3) {
    return;
  }

  // @ts-expect-error - runtime is injected by wails
  window.runtime.WindowSetBackgroundColour(rgb[0], rgb[1], rgb[2], 255);
}

export function ThemeProvider({
  children,
  defaultTheme = "default",
  defaultDarkMode = "dark",
  storageKey = "vite-ui-theme",
  ...props
}: ThemeProviderProps) {
  const [theme, setTheme] = useState<Theme>(() => {
    const themeFromStorage = localStorage.getItem(storageKey) as Theme;
    return Themes.includes(themeFromStorage) ? themeFromStorage : defaultTheme;
  });

  const [darkMode, setDarkMode] = useState<DarkMode>(() => {
    return (
      (localStorage.getItem(storageKey + "-darkmode") as DarkMode) ||
      defaultDarkMode
    );
  });

  const [isDarkMode, setIsDarkMode] = useState<boolean>(false);

  useEffect(() => {
    const root = window.document.documentElement;

    // Find and remove classes that start with 'theme-'
    const classList = root.classList;
    classList.forEach((className) => {
      if (className.startsWith("theme-")) {
        classList.remove(className);
      }
    });

    classList.add(`theme-${theme}`);

    let prefersDark = false;
    if (darkMode == "system") {
      prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
    } else {
      prefersDark = darkMode === "dark";
    }

    // eslint-disable-next-line react-hooks/set-state-in-effect
    setIsDarkMode(prefersDark);

    if (prefersDark) {
      classList.add("dark");
    } else {
      classList.remove("dark");
    }

    syncNativeWindowBackground();
  }, [theme, darkMode]);

  const value = {
    theme,
    setTheme: (theme: Theme) => {
      localStorage.setItem(storageKey, theme);
      setTheme(theme);
    },
    darkMode,
    setDarkMode: (darkMode: DarkMode) => {
      localStorage.setItem(storageKey + "-darkmode", darkMode);
      setDarkMode(darkMode);
    },
    isDarkMode,
  };

  return (
    <ThemeProviderContext.Provider {...props} value={value}>
      {children}
    </ThemeProviderContext.Provider>
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export const useTheme = () => {
  const context = useContext(ThemeProviderContext);

  if (context === undefined) {
    throw new Error("useTheme must be used within a ThemeProvider");
  }

  return context;
};
