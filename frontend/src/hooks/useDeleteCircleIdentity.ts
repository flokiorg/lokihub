import React from "react";
import { toast } from "sonner";
import { useSWRConfig } from "swr";

import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// useDeleteCircleIdentity deletes a standalone CircleIdentity. The backend
// refuses (400) if any circle_hub app still references it, so the
// caller only needs to keep its own UI disabled for in-use identities —
// this hook surfaces the failure via toast either way.
export function useDeleteCircleIdentity() {
  const [isDeleting, setDeleting] = React.useState(false);
  const { mutate } = useSWRConfig();

  const deleteIdentity = React.useCallback(
    async (id: number) => {
      setDeleting(true);
      try {
        await request(`/api/circle-identities/${id}`, { method: "DELETE" });
        await mutate(
          (key) =>
            typeof key === "string" && key.startsWith("/api/circle-identities"),
          undefined,
          { revalidate: true }
        );
        toast("Identity deleted");
        return true;
      } catch (error) {
        await handleRequestError("Failed to delete identity", error);
        return false;
      } finally {
        setDeleting(false);
      }
    },
    [mutate]
  );

  return React.useMemo(
    () => ({ deleteIdentity, isDeleting }),
    [deleteIdentity, isDeleting]
  );
}
