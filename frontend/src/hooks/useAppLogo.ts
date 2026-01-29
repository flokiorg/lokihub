export function useAppLogo(appId?: string) {
  if (!appId) return undefined;
  return `/api/appstore/logos/${appId}`;
}
