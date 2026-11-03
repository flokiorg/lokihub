import { useInfo } from "src/hooks/useInfo";
import { useHubInfo } from "src/hooks/useHubInfo";
import { compareVersions } from "src/utils/compareVersions";

export function useBanner() {
  const { data: info } = useInfo();
  const { data: hubInfo } = useHubInfo();

  const currentVersion = info?.version;
  const latestVersion = hubInfo?.version;

  // Check if user has dismissed this specific version
  const dismissedVersion =
    typeof window !== "undefined"
      ? localStorage.getItem("dismissedUpdateVersion")
      : null;

  // Show banner if:
  // 1. We have both versions
  // 2. Latest version is greater than current version
  // 3. User hasn't dismissed this specific version
  const hasUpdate =
    currentVersion &&
    latestVersion &&
    compareVersions(latestVersion, currentVersion) > 0;

  const showBanner = hasUpdate && dismissedVersion !== latestVersion;

  const dismissBanner = () => {
    if (latestVersion && typeof window !== "undefined") {
      localStorage.setItem("dismissedUpdateVersion", latestVersion);
      // Force re-render by updating a dummy state would be needed
      // but since this is just dismissing, the hook consumer should handle UI update
      window.location.reload(); // Simple approach - reload to hide banner
    }
  };

  return { showBanner, dismissBanner };
}
