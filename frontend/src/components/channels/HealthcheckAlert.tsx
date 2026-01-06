import { AlertTriangleIcon, CheckCircle2Icon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { useHealthCheck } from "src/hooks/useHealthCheck";
import { HealthAlarm } from "src/types";

export function HealthCheckAlert() {
  const { data: health } = useHealthCheck();

  const ok = !health?.alarms?.length && !health?.message;

  if (!health) {
    return null;
  }

  if (ok) {
    return null;
  }

  function getAlarmTitle(alarm: HealthAlarm) {
    // TODO: could show extra data from alarm.rawDetails
    // for some alarm types
    try {
      switch (alarm.kind) {
        case "channels_offline":
          return "One or more channels are offline";
        case "node_not_ready":
          return "Node is not ready";
        case "nostr_relay_offline":
          return (
            "Could not connect to relay: " +
            (alarm.rawDetails as string[]).join(", ")
          );
        case "vss_no_subscription":
          return "Your lightning channel data is stored encrypted by Loki's Versioned Storage Service which is a paid feature. Restart your subscription or send your funds to another wallet as soon as possible.";
      }
    } catch (error) {
      console.error("failed to parse alarm details", alarm.kind, error);
    }
    return alarm.kind || "Unknown";
  }

  return (
    <>
      <Alert className="animate-highlight">
        {ok ? (
          <>
            <CheckCircle2Icon className="h-4 w-4" />
            <AlertTitle>Lokihub is running smoothly</AlertTitle>
          </>
        ) : (
          <>
            <AlertTriangleIcon className="h-4 w-4" />
            <AlertTitle>
              {health.alarms.length} issues impacting your hub were found
            </AlertTitle>
          </>
        )}
        <AlertDescription>
          {health.alarms?.length > 0 && (
            <ul className="mt-2 whitespace-pre-wrap list-disc list-inside">
              {health.alarms?.map((alarm) => (
                <li key={alarm.kind}>{getAlarmTitle(alarm)}</li>
              ))}
            </ul>
          )}
          {health.message && (
             <div className="mt-2 font-medium">{health.message}</div>
          )}
        </AlertDescription>
      </Alert>
    </>
  );
}
