import React from "react";
import { toast } from "sonner";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { CurrencyInput } from "src/components/CurrencyInput";
import { useChannels } from "src/hooks/useChannels";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
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
  const { displayFormat, scaleInputAmount, parseInputAmount } = useUnit();
  const currentBaseFeeLoki: number = channel.forwardingFeeBaseMloki / 1000;
  const currentFeePPM: number = channel.forwardingFeeProportionalMillionths;

  const [inputUnit, setInputUnit] = useInputUnit(currentBaseFeeLoki);

  const [baseFeeDisplay, setBaseFeeDisplay] = React.useState(
    scaleInputAmount(currentBaseFeeLoki, displayFormat === "loki" ? "loki" : "FLC").toString()
  );

  const handleInputUnitChange = (newUnit: "FLC" | "loki") => {
    if (baseFeeDisplay) {
      const amountLoki = parseInputAmount(parseFloat(baseFeeDisplay), inputUnit);
      if (!isNaN(amountLoki)) {
        const newAmount = scaleInputAmount(amountLoki, newUnit);
        setBaseFeeDisplay(newAmount.toString());
      }
    }
    setInputUnit(newUnit);
  };

  const [
    forwardingFeeProportionalMillionths,
    setForwardingFeeProportionalMillionths,
  ] = React.useState(
    currentFeePPM !== undefined ? currentFeePPM.toString() : ""
  );
  const { mutate: reloadChannels } = useChannels();

  async function updateFee() {
    try {
      const forwardingFeeBaseMloki = parseInputAmount(parseFloat(baseFeeDisplay), inputUnit) * 1000;

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
          <p className="mb-4 text-foreground">
            Adjust the fee you charge for each payment routed through this
            channel. A high fee (e.g. {scaleInputAmount(100_000, inputUnit)} {inputUnit}) can be set to prevent
            unwanted routing. No matter the fee, you can still receive
            payments.{" "}
          </p>
          <Label htmlFor="fee" className="block mb-2">
            Base Routing Fee
          </Label>
          <CurrencyInput
            id="fee"
            required
            autoFocus
            amount={baseFeeDisplay}
            onAmountChange={setBaseFeeDisplay}
            inputUnit={inputUnit}
            onInputUnitChange={handleInputUnitChange}
            min={0}
          />
          <Label htmlFor="ppm" className="block mt-4 mb-2">
            PPM Fee (1 PPM = 1 per 1 million)
          </Label>
          <Input
            id="ppm"
            name="ppm"
            type="number"
            required
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
            parseInputAmount(parseFloat(baseFeeDisplay), inputUnit) === currentBaseFeeLoki &&
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
