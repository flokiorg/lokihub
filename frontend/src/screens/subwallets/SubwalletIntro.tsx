import {
    CirclePlusIcon,
    HandCoins,
    HelpCircle,
    TriangleAlert,
    Wallet2,
} from "lucide-react";
import AppHeader from "src/components/AppHeader";

import SubWalletDark from "src/assets/illustrations/sub-wallet-dark.svg?react";
import SubWalletLight from "src/assets/illustrations/sub-wallet-light.svg?react";
import ResponsiveLinkButton from "src/components/ResponsiveLinkButton";
import { SubWalletInfoDialog } from "src/components/SubWalletInfoDialog";
import { Button } from "src/components/ui/button";
import { LinkButton } from "src/components/ui/custom/link-button";

export function SubwalletIntro() {
  return (
    <div className="grid gap-4">
      <AppHeader
        title="Sub-wallets"
        description="Create sub-wallets for yourself, friends, family or coworkers"
        contentRight={
          <>
            <SubWalletInfoDialog
              trigger={
                <Button variant="outline" size="icon">
                  <HelpCircle className="size-4" />
                </Button>
              }
            />
            <ResponsiveLinkButton
              to="/sub-wallets/new"
              icon={CirclePlusIcon}
              text="New Sub-wallet"
            />
          </>
        }
      />
      <div>
        <div className="flex flex-col gap-6 max-w-(--breakpoint-md)">
          <div className="mb-2">
            <SubWalletDark className="w-72 hidden dark:block" />
            <SubWalletLight className="w-72 dark:hidden" />
          </div>
          <div>
            <div className="flex flex-row gap-3">
              <Wallet2 className="size-6" />
              <div className="font-medium">
                Sub-wallets are separate wallets hosted by your Lokihub
              </div>
            </div>
            <div className="ml-9 text-muted-foreground text-sm">
              Each sub-wallet has its own balance and can be used as a separate
              wallet that can be connected to Loki Account or any app.
            </div>
          </div>
          <div>
            <div className="flex flex-row gap-3">
              <HandCoins className="size-6" />
              <div className="font-medium">
                Sub-wallets depend on your Lokihub spending balance and receive
                limit
              </div>
            </div>
            <div className="ml-9 text-muted-foreground text-sm">
              Sub-wallets are using your Hubs node liquidity. They can receive
              funds as long as you have enough receive limit in your channels.
            </div>
          </div>
          <div>
            <div className="flex flex-row gap-3">
              <TriangleAlert className="size-6" />
              <div className="font-medium">
                Be wary of spending sub-wallets funds
              </div>
            </div>
            <div className="ml-9 text-muted-foreground text-sm">
              Make sure you always maintain enough funds in your spending
              balance to prevent sub-wallets becoming unspendable. Sub-wallet
              payments might fail if the amount isn't available in your spending
              balance.
            </div>
          </div>
          <div>
            <LinkButton to="/sub-wallets/new" className="mt-4">
              Create Sub-wallet
            </LinkButton>
          </div>
        </div>
      </div>
    </div>
  );
}
