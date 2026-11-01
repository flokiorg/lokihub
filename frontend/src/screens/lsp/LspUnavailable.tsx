import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { AppError } from "src/utils/request";

// LspUnavailable renders the graceful-degradation state (plan §3.4): when lspd
// is configured but unreachable or the admin key is rejected, the Hub proxy
// returns 503 (not configured) / 502 (unreachable or key mismatch). Screens
// render this instead of crashing, so the nav item can stay visible while lspd
// is down.
export function LspUnavailable({ error }: { error: unknown }) {
  const { title, body } = describe(error);
  return (
    <Alert variant="warning">
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>{body}</AlertDescription>
    </Alert>
  );
}

function describe(error: unknown): { title: string; body: string } {
  if (error instanceof AppError) {
    if (error.status === 503) {
      return {
        title: "LSP service not configured",
        body: "No LSP daemon is attached to this Hub.",
      };
    }
    if (error.status === 502) {
      return {
        title: "LSP service unavailable",
        body:
          error.message ||
          "The Hub could not reach the LSP daemon, or its admin key was rejected.",
      };
    }
    return {
      title: "Could not load LSP data",
      body: error.message || `HTTP ${error.status ?? ""}`.trim(),
    };
  }
  return {
    title: "Could not load LSP data",
    body: "An unexpected error occurred talking to the LSP service.",
  };
}
