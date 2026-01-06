import {
    AlertDialog,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogTrigger,
} from "src/components/ui/alert-dialog";

type SubWalletInfoDialogProps = {
  trigger: React.ReactNode;
};

export function SubWalletInfoDialog({ trigger }: SubWalletInfoDialogProps) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        {trigger}
      </AlertDialogTrigger>
      <AlertDialogContent className="max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>About Sub-wallets</AlertDialogTitle>
          <div className="flex flex-col gap-4 text-muted-foreground text-sm">
            <p>
              Sub-wallets are separate balances within your wallet. You can use
              them to budget your spending for specific apps, or to onboard
              friends and family to Flokicoin by giving them their own wallet.
            </p>
            <p>
              Each sub-wallet has its own credentials and can be connected to
              apps independently.
            </p>
          </div>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Close</AlertDialogCancel>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
