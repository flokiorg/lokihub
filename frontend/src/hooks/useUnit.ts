import { useInfo } from "src/hooks/useInfo";
import { getFlokicoinUnit } from "src/utils/flokicoinFormatting";

/**
 * Hook to get the current Flokicoin unit (FLC or loki)
 */
export function useUnit() {
  const { data: info } = useInfo();
  
  if (!info) {
    return "loki"; // Default fallback
  }

  return getFlokicoinUnit(info.flokicoinDisplayFormat);
}
