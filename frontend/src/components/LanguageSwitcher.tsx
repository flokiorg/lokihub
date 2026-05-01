import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { Label } from "src/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { useLocale } from "src/hooks/useLocale";
import type { SupportedLanguageCode } from "src/i18n";

export function LanguageSwitcher() {
  const { currentLanguage, changeLanguage, supportedLanguages } = useLocale();
  const { t } = useTranslation("settings");

  return (
    <div className="grid gap-2">
      <Label htmlFor="language">{t("language.label")}</Label>
      <Select
        value={currentLanguage}
        onValueChange={async (value) => {
          await changeLanguage(value as SupportedLanguageCode);
          toast(t("language.updated"));
        }}
      >
        <SelectTrigger className="w-full md:w-60">
          <SelectValue placeholder={t("language.label")} />
        </SelectTrigger>
        <SelectContent>
          {supportedLanguages.map((lang) => (
            <SelectItem key={lang.code} value={lang.code}>
              {lang.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
