
import {
    AlertDialog,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogTrigger,
} from "src/components/ui/alert-dialog";
import { Separator } from "src/components/ui/separator";

type NodeHealthInfoDialogProps = {
  trigger: React.ReactNode;
};

export function NodeHealthInfoDialog({ trigger }: NodeHealthInfoDialogProps) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <div className="cursor-pointer inline">{trigger}</div>
      </AlertDialogTrigger>
      <AlertDialogContent className="max-w-2xl">
        <AlertDialogHeader>
          <AlertDialogTitle>Node Health</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="flex flex-col gap-4 text-justify">
              <p>
                The health indicator shows how well-positioned your node is to
                route payments reliably. Ideally, you should aim for a score
                above <strong>60%</strong>. Use the tips below to improve your
                node health.
              </p>

              <Separator />

              <h3 className="font-semibold text-foreground">
                Spending Balance & Receiving Capacity
              </h3>
              <p>
                Ensure you have enough funds to both send (Spending Balance) and
                receive (Receiving Capacity) payments. Channels that are
                completely full or empty can restrict your ability to transact.
              </p>

              <h3 className="font-semibold text-foreground">
                Channel Partners
              </h3>
              <p>
                Connect to multiple channel partners. Relying on a single node
                can lead to failures if that partner goes offline. Diverse
                connections increase reliability.
              </p>

              <h3 className="font-semibold text-foreground">
                Channel Size & Quality
              </h3>
              <p>
                Larger channels are generally better for handling various payment
                sizes. Choose well-connected and reliable peers to ensure your
                transactions route successfully.
              </p>


            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Close</AlertDialogCancel>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
