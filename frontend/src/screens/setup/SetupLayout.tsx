import { ChevronLeftIcon } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Button } from "src/components/ui/button";
import { cn } from "src/lib/utils";

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

  return (
    <div className="h-full w-full flex flex-col bg-background">
      {/* Main Content Area - Centered */}
      <div className="flex-1 flex flex-col items-center w-full px-5 py-6 md:py-12">
        <div className={cn("w-full max-w-md flex flex-col", contentClassName)}>
          {shouldShowBack && (
            <div className="mb-4 self-start">
              <Button
                variant="ghost"
                onClick={handleBack}
                className="pl-0 text-muted-foreground hover:text-foreground -ml-2"
              >
                <ChevronLeftIcon className="w-5 h-5 mr-1" />
                Back
              </Button>
            </div>
          )}
          {children}
        </div>
      </div>
    </div>
  );
}
