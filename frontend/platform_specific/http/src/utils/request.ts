import { getAuthToken } from "src/lib/auth";
import { ErrorResponse } from "src/types";

// Mirrors StartupErrorCode in wails/startup_error.go — not currently sent by
// the http backend, but honored here for parity with the wails AppError.
const STARTUP_ERROR_CODE = "startup_failed";

export class AppError extends Error {
  status?: number;
  url?: string;
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

export const request = async <T>(
  ...args: Parameters<typeof fetch>
): Promise<T | undefined> => {
  if (import.meta.env.BASE_URL !== "/") {
    // if running on a subpath, include the subpath in the request URL
    // BASE_URL is set via process.env.BASE_PATH, see https://vite.dev/guide/build#public-base-path
    args[0] = import.meta.env.BASE_URL + args[0];
  }

  const token = getAuthToken();
  if (token) {
    if (!args[1]) {
      args[1] = {};
    }
    args[1].headers = {
      ...args[1].headers,
      Authorization: `Bearer ${token}`,
    };
  }

  try {
    const fetchResponse = await fetch(...args);

    let body: T | undefined;
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
    return body;
  } catch (error) {
    console.error("Failed to fetch", error);
    throw error;
  }
};
