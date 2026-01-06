import Tick from "src/assets/illustrations/tick.svg?react";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { LinkButton } from "src/components/ui/custom/link-button";

export function OpenedFirstChannel() {
  return (
    <div className="flex flex-col items-center justify-center gap-10 p-5 w-full max-w-md">
      <TwoColumnLayoutHeader
        title="Channel Opened"
        description="Your new lightning channel is ready to use."
      />

      <Tick className="w-48" />

      <LinkButton to="/wallet/receive" className="flex w-full justify-center">
        Receive Your First Payment
      </LinkButton>
    </div>
  );
}
