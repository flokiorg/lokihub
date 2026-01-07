export const isHttpMode = () => {
  return (
    window.location.protocol.startsWith("http") &&
    !window.location.hostname.startsWith("wails") &&
    // @ts-expect-error - wails is injected by wails
    !window.wails &&
    // @ts-expect-error - runtime is injected by wails
    !window.runtime
  );
};
