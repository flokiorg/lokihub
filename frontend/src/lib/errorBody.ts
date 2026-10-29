import type { ErrorResponse } from "src/types";

// errorBody is the error payload a refusal carries, on either transport: the
// HTTP error body, or the body of a desktop router response whose error is
// set. A payload that is not an object with a message is none.
export function errorBody(body: unknown): ErrorResponse | undefined {
  if (
    body &&
    typeof body === "object" &&
    typeof (body as ErrorResponse).message === "string"
  ) {
    return body as ErrorResponse;
  }
  return undefined;
}
