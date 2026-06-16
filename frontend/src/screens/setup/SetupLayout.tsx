import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { Button } from "src/components/ui/button";
import { cn } from "src/lib/utils";
import { useLocale } from "src/hooks/useLocale";

type Props = {
  children: React.ReactNode;
  title?: string; // Optional title for tracking/debugging or potentially header display
  backTo?: string; // Path to navigate back to
  onBack?: () => void; // Custom back handler (takes precedence over backTo)
  showBack?: boolean; // explicitly show/hide back button (default: check backTo || onBack)
  contentClassName?: string; // Allow wider content for selection screens
};

export function SetupLayout({
  children,
  backTo,
  onBack,
  showBack,
  contentClassName
}: Props) {
  const navigate = useNavigate();
  const { isRTL } = useLocale();
  const { t } = useTranslation("common");

  const handleBack = () => {
    if (onBack) {
      onBack();
    } else if (backTo) {
      navigate(backTo);
    } else {
      navigate(-1);
    }
  };

  const shouldShowBack = showBack !== undefined ? showBack : (!!backTo || !!onBack);
  const BackIcon = isRTL ? ChevronRightIcon : ChevronLeftIcon;

  return (
    <div className="h-full w-full flex flex-col bg-background">
      <div className="flex-1 flex flex-col items-center w-full px-5 py-6 md:py-12 relative">
        <div className={cn("w-full max-w-md flex flex-col", contentClassName)}>
          {shouldShowBack && (
            <div className="mb-4 self-start">
              <Button
                variant="ghost"
                onClick={handleBack}
                className="ps-0 text-muted-foreground hover:text-foreground -ms-2"
              >
                <BackIcon className="w-5 h-5 me-1" />
                {t("actions.back")}
              </Button>
            </div>
          )}
          {children}
        </div>
      </div>
    </div>
  );
}
