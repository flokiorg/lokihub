import { useCallback } from "react";
import { useTranslation } from "react-i18next";
import {
  RTL_LANGUAGES,
  SUPPORTED_LANGUAGES,
  type SupportedLanguageCode,
} from "src/i18n";

/**
 * Custom hook for locale management.
 * Handles language switching, RTL direction updates, and document lang attribute.
 */
export function useLocale() {
  const { i18n } = useTranslation();

  const currentLanguage =
    (i18n.language as SupportedLanguageCode) || "en";

  const isRTL = RTL_LANGUAGES.includes(currentLanguage);

  const changeLanguage = useCallback(
    async (languageCode: SupportedLanguageCode) => {
      await i18n.changeLanguage(languageCode);

      // Update document direction for RTL languages
      const dir = RTL_LANGUAGES.includes(languageCode) ? "rtl" : "ltr";
      document.documentElement.dir = dir;
      document.documentElement.lang = languageCode;
    },
    [i18n]
  );

  return {
    currentLanguage,
    isRTL,
    changeLanguage,
    supportedLanguages: SUPPORTED_LANGUAGES,
  };
}
