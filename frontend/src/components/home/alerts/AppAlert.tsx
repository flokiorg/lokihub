import { X } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import {
    Card,
    CardDescription,
    CardHeader,
    CardTitle,
} from "src/components/ui/card";
import { useAppLogo } from "src/hooks/useAppLogo";

type Props = {
  app: AppStoreApp;
  type: "new" | "updated";
  onDismiss: (appId: string) => void;
};

export default function AppAlert({ app, type, onDismiss }: Props) {
  const navigate = useNavigate();
  const logoSrc = useAppLogo(app.id);

  return (
    <Card className="relative overflow-hidden mb-4 rounded-xl">
      <button
        onClick={(e) => {
          e.stopPropagation();
          onDismiss(app.id);
        }}
        className="absolute right-4 top-4 hover:opacity-70 transition-opacity"
      >
        <X className="w-5 h-5 text-muted-foreground" />
      </button>
      <div
        className="cursor-pointer"
        onClick={() => navigate(`/appstore/${app.id}`)}
      >
        <CardHeader className="flex flex-row items-center gap-4">
          <div className="w-12 h-12 rounded-lg overflow-hidden shrink-0">
            {app.logo && logoSrc ? (
              <img
                src={logoSrc}
                alt={app.title}
                className="w-full h-full object-cover"
              />
            ) : (
              <div className="w-full h-full bg-primary/20 flex items-center justify-center text-xl font-bold">
                {app.title[0]}
              </div>
            )}
          </div>
          <div>
            <CardTitle className="text-lg">
              {type === "new" ? "New App Available:" : "App Updated:"} {app.title}
            </CardTitle>
            <CardDescription>{app.description}</CardDescription>
          </div>
        </CardHeader>
      </div>
    </Card>
  );
}
