import { toast } from "sonner";
import { AppError } from "src/utils/request";

export function handleRequestError(message: string, error: unknown) {
  console.error(message, error);
  let description: string | undefined;
  if (error instanceof AppError) {
    description = error.status
      ? `HTTP ${error.status}: ${error.message}`
      : error.message;
  } else if (isErrorWithMessage(error)) {
    description = error.message;
  }
  toast.error(message, { description });
}
type ErrorWithMessage = {
  message: string;
};

function isErrorWithMessage(error: unknown): error is ErrorWithMessage {
  return (
    typeof error === "object" &&
    error !== null &&
    "message" in error &&
    typeof (error as Record<string, unknown>).message === "string"
  );
}
