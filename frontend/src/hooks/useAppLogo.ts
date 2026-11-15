export function useAppLogo(appId?: string) {
  return appId ? `/api/appstore/logos/${appId}` : undefined;
}
