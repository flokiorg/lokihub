import { AppError, request } from "./request";

// All SWR reads are idempotent GETs, so aborting a hung one and surfacing an
// error is safe — unlike the shared `request()` used directly for mutations
// (e.g. paying an invoice), where a client-side timeout could report failure
// while the backend still completes the action.
const SWR_FETCH_TIMEOUT_MS = 20_000;

export const swrFetcher = async (...args: Parameters<typeof fetch>) => {
  const [url, init] = args;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), SWR_FETCH_TIMEOUT_MS);

  try {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    return (await request(url, { ...init, signal: controller.signal })) as any;
  } catch (error) {
    if (controller.signal.aborted) {
      throw new AppError(
        "Timed out waiting for a response from the server",
        undefined,
        url.toString()
      );
    }
    throw error;
  } finally {
    clearTimeout(timeout);
  }
};
