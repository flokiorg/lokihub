import React from "react";
import { toast } from "sonner";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { useChannels } from "src/hooks/useChannels";
import { Channel, UpdateChannelRequest } from "src/types";
import { request } from "src/utils/request";
import {
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
} from "./ui/alert-dialog";

type Props = {
  channel: Channel;
};

export function RoutingFeeDialogContent({ channel }: Props) {
  const currentBaseFeeLoki: number = Math.floor(
    channel.forwardingFeeBaseMloki / 1000
  );
  const currentFeePPM: number = channel.forwardingFeeProportionalMillionths;

  const [baseFeeLoki, setBaseFeeLoki] = React.useState(
    currentBaseFeeLoki !== undefined ? currentBaseFeeLoki.toString() : ""
  );
  const [
    forwardingFeeProportionalMillionths,
    setForwardingFeeProportionalMillionths,
  ] = React.useState(
    currentFeePPM !== undefined ? currentFeePPM.toString() : ""
  );
  const { mutate: reloadChannels } = useChannels();

  async function updateFee() {
    try {
      const forwardingFeeBaseMloki = +baseFeeLoki * 1000;

      console.info(
        `🎬 Updating channel ${channel.id} with ${channel.remotePubkey}`
      );

      await request(
        `/api/peers/${channel.remotePubkey}/channels/${channel.id}`,
        {
          method: "PATCH",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify({
            forwardingFeeBaseMloki: forwardingFeeBaseMloki,
            forwardingFeeProportionalMillionths:
              +forwardingFeeProportionalMillionths,
          } as UpdateChannelRequest),
        }
      );

      await reloadChannels();
      toast("Successfully updated channel");
    } catch (error) {
      console.error(error);
      toast.error("Something went wrong", {
        description: "" + error,
      });
    }
  }

  return (
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>Update Channel Routing Fee</AlertDialogTitle>
        <AlertDialogDescription>
          <p className="mb-4">
            Adjust the fee you charge for each payment routed through this
            channel. A high fee (e.g. 100,000 loki) can be set to prevent
            unwanted routing. No matter the fee, you can still receive
            payments.{" "}
          </p>
          <Label htmlFor="fee" className="block mb-2">
            Base Routing Fee (loki)
          </Label>
          <Input
            id="fee"
            name="fee"
            type="number"
            required
            autoFocus
            min={0}
            value={baseFeeLoki}
            onChange={(e) => {
              setBaseFeeLoki(e.target.value.trim());
            }}
          />
          <Label htmlFor="fee" className="block mt-4 mb-2">
            PPM Fee (1 PPM = 1 per 1 million loki)
          </Label>
          <Input
            id="fee"
            name="fee"
            type="number"
            required
            autoFocus
            min={0}
            value={forwardingFeeProportionalMillionths}
            onChange={(e) => {
              setForwardingFeeProportionalMillionths(e.target.value.trim());
            }}
          />
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction
          disabled={
            (parseInt(baseFeeLoki) || 0) === currentBaseFeeLoki &&
            (parseInt(forwardingFeeProportionalMillionths) || 0) ===
              currentFeePPM
          }
          onClick={updateFee}
        >
          Confirm
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}
