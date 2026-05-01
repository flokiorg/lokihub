import { Loader2Icon, LoaderIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "src/lib/utils";

function Loading({
  className,
  variant = "loader2",
}: {
  className?: string;
  variant?: "loader2" | "loader";
}) {
  const { t } = useTranslation("common");
  const Component = variant === "loader2" ? Loader2Icon : LoaderIcon;

  return (
    <>
      <Component
        className={cn(
          "h-6 w-6",
          variant === "loader2" ? "animate-spin" : "animate-spin-slow",
          className
        )}
      >
        <span className="sr-only">{t("loading")}</span>
      </Component>
    </>
  );
}

export default Loading;
