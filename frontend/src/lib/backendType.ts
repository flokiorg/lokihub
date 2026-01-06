import { BackendType } from "src/types";

type BackendTypeConfig = {
  hasMnemonic: boolean;
  hasChannelManagement: boolean;
  hasNodeBackup: boolean;
};

export const backendTypeConfigs: Record<BackendType, BackendTypeConfig> = {
  FLND: {
    hasMnemonic: false,
    hasChannelManagement: true,
    hasNodeBackup: false,
  },
};
