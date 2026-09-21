import { WailsRequestRouter } from "wailsjs/go/wails/WailsApp";
import { ErrorResponse } from "src/types";

// Mirrors StartupErrorCode in wails/startup_error.go.
const STARTUP_ERROR_CODE = "startup_failed";

export class AppError extends Error {
  status?: number;
  url?: string;
  // Only set when the backend failed to start (see wails/startup_error.go) —
  // lets App.tsx show the dedicated startup-error screen instead of the
  // generic one.
  isStartupError?: boolean;
  body?: ErrorResponse;

  constructor(
    message: string,
    status?: number,
    url?: string,
    body?: ErrorResponse
  ) {
    super(message);
    this.name = "AppError";
    this.status = status;
    this.url = url;
    this.body = body;
    this.isStartupError = body?.code === STARTUP_ERROR_CODE;
  }
}

// The startup-error window (see wails/startup_error.go) never binds WailsApp,
// so WailsRequestRouter isn't available over IPC yet — only once the backend
// starts successfully and LaunchWailsApp binds it.
function hasWailsBinding(): boolean {
  // @ts-expect-error - go is injected by wails once methods are bound
  return !!window.go?.wails?.WailsApp?.WailsRequestRouter;
}

// fetchDirect talks to the same AssetServer HTTP handler over real fetch
// instead of the WailsRequestRouter IPC bridge, for use before WailsApp is
// bound (i.e. while showing the startup-error screen).
async function fetchDirect<T>(
  ...args: Parameters<typeof fetch>
): Promise<T | undefined> {
  const fetchResponse = await fetch(...args);

  let body: unknown;
  if (fetchResponse.status !== 204) {
    const text = await fetchResponse.text();
    try {
      body = text ? JSON.parse(text) : undefined;
    } catch (error) {
      console.error("Failed to parse JSON response", error);
    }
  }

  if (!fetchResponse.ok) {
    throw new AppError(
      (body as ErrorResponse)?.message || "Unknown error",
      fetchResponse.status,
      args[0].toString(),
      body as ErrorResponse
    );
  }

  return body as T;
}

export const request = async <T>(
  ...args: Parameters<typeof fetch>
): Promise<T | undefined> => {
  try {
    if (!hasWailsBinding()) {
      return await fetchDirect<T>(...args);
    }

    const res = await WailsRequestRouter(
      args[0].toString(),
      args[1]?.method || "GET",
      args[1]?.body?.toString() || ""
    );

    console.info("Wails request", ...args, res);
    if (res.error) {
      throw new Error(res.error);
    }

    return res.body;
  } catch (error) {
    console.error("Failed to fetch", error);
    throw error;
  }
};
