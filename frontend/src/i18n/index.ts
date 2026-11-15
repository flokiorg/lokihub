import i18n from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import resourcesToBackend from "i18next-resources-to-backend";
import { initReactI18next } from "react-i18next";

export const SUPPORTED_LANGUAGES = [
  { code: "en", name: "English", dir: "ltr" },
  { code: "ar", name: "العربية", dir: "rtl" },
  { code: "fa", name: "فارسی", dir: "rtl" },
  { code: "zh", name: "中文", dir: "ltr" },
  { code: "ja", name: "日本語", dir: "ltr" },
  { code: "ko", name: "한국어", dir: "ltr" },
  { code: "es", name: "Español", dir: "ltr" },
  { code: "ru", name: "Русский", dir: "ltr" },
] as const;

export type SupportedLanguageCode =
  (typeof SUPPORTED_LANGUAGES)[number]["code"];

export const RTL_LANGUAGES: SupportedLanguageCode[] = ["ar", "fa"];

export const DEFAULT_LANGUAGE: SupportedLanguageCode = "en";

export const NAMESPACES = [
  "common",
  "home",
  "settings",
  "wallet",
  "setup",
  "channels",
  "apps",
  "help",
  "circles",
  "listings",
  "lsp",
] as const;

export type Namespace = (typeof NAMESPACES)[number];

i18n
  .use(
    resourcesToBackend(
      (language: string, namespace: string) =>
        import(`./locales/${language}/${namespace}.json`)
    )
  )
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    defaultNS: "common",
    ns: NAMESPACES,
    fallbackLng: DEFAULT_LANGUAGE,
    supportedLngs: SUPPORTED_LANGUAGES.map((l) => l.code),
    detection: {
      order: ["localStorage", "navigator"],
      lookupLocalStorage: "lokihub-lang",
      caches: ["localStorage"],
    },
    interpolation: {
      escapeValue: false, // React already escapes
      // Native-asset identity tokens, resolved globally so locale strings can
      // use {{unit}}/{{ticker}}/{{subunit}}/{{chain}} without every call site
      // passing them. These embedded Flokicoin defaults keep output byte-for-byte
      // until the hub's nativeAsset loads and overrides them (see
      // useNativeAssetI18n).
      defaultVariables: {
        unit: "loki",
        ticker: "FLC",
        subunit: "mloki",
        chain: "Flokicoin",
      },
    },
    react: {
      useSuspense: true,
    },
  });

export default i18n;
