import { AlertCircle } from "lucide-react";
import ExternalLink from "src/components/ExternalLink";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";

/**
 * Shared header for service configuration sections.
 * Displays description of community services and warning about trusting providers.
 * Used in: SetupServices, Settings, GlobalError
 */
export function ServiceConfigurationHeader() {
  return (
    <div className="space-y-4">
      <div className="text-muted-foreground text-sm">
        These service suggestions are provided by the community. To suggest changes or add new services, please
        submit a pull request to the{" "}
        <ExternalLink
          to="https://github.com/flokiorg/lokihub-services"
          className="underline underline-offset-4"
        >
          lokihub-services
        </ExternalLink>{" "}
        repository.
      </div>
      <Alert variant="warning">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>Warning</AlertTitle>
        <AlertDescription>
          You are responsible for the services you connect to. Ensure you trust
          the providers of these URLs.
        </AlertDescription>
      </Alert>
    </div>
  );
}
