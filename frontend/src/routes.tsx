import { Navigate, RouteObject } from "react-router-dom";
import AppLayout from "src/components/layouts/AppLayout";
import SettingsLayout from "src/components/layouts/SettingsLayout";
import TwoColumnFullScreenLayout from "src/components/layouts/TwoColumnFullScreenLayout";
import { DefaultRedirect } from "src/components/redirects/DefaultRedirect";
import { HomeRedirect } from "src/components/redirects/HomeRedirect";
import { SetupRedirect } from "src/components/redirects/SetupRedirect";
import { StartRedirect } from "src/components/redirects/StartRedirect";
import { CreateNodeMigrationFileSuccess } from "src/screens/CreateNodeMigrationFileSuccess";
import Home from "src/screens/Home";
import { Intro } from "src/screens/Intro";
import { MigrateNode } from "src/screens/MigrateNode";
import NotFound from "src/screens/NotFound";
import Start from "src/screens/Start";
import Unlock from "src/screens/Unlock";
import { Welcome } from "src/screens/Welcome";
import AppDetails from "src/screens/apps/AppDetails";
import { AppsCleanup } from "src/screens/apps/AppsCleanup";
import { Connections } from "src/screens/apps/Connections";
import NewApp from "src/screens/apps/NewApp";
import { AppStoreDetail } from "src/screens/appstore/AppStoreDetail";
import Channels from "src/screens/channels/Channels";
import { CurrentChannelOrder } from "src/screens/channels/CurrentChannelOrder";
import IncreaseOutgoingCapacity from "src/screens/channels/IncreaseOutgoingCapacity";
import OrderChannel from "src/screens/channels/OrderChannel";
import { AutoChannel } from "src/screens/channels/auto/AutoChannel";
import { OpenedAutoChannel } from "src/screens/channels/auto/OpenedAutoChannel";
import { OpeningAutoChannel } from "src/screens/channels/auto/OpeningAutoChannel";
import { FirstChannel } from "src/screens/channels/first/FirstChannel";
import { OpenedFirstChannel } from "src/screens/channels/first/OpenedFirstChannel";
import { OpeningFirstChannel } from "src/screens/channels/first/OpeningFirstChannel";
import { FAQ } from "src/screens/help/FAQ";

import DepositFlokicoin from "src/screens/onchain/DepositFlokicoin";
import ConnectPeer from "src/screens/peers/ConnectPeer";
import Peers from "src/screens/peers/Peers";
import { About } from "src/screens/settings/About";
import { AutoUnlock } from "src/screens/settings/AutoUnlock";
import Backup from "src/screens/settings/Backup";
import { ChangeUnlockPassword } from "src/screens/settings/ChangeUnlockPassword";
import DebugTools from "src/screens/settings/DebugTools";
import DeveloperSettings from "src/screens/settings/DeveloperSettings";
import { Services } from "src/screens/settings/Services";
import Settings from "src/screens/settings/Settings";

import { RestoreNode } from "src/screens/setup/RestoreNode";
import { SetupFinish } from "src/screens/setup/SetupFinish";
import { SetupPassword } from "src/screens/setup/SetupPassword";
import { SetupSecurity } from "src/screens/setup/SetupSecurity";
import { SetupServices } from "src/screens/setup/SetupServices";
import { LNDForm } from "src/screens/setup/node/LNDForm";
import { NewSubwallet } from "src/screens/subwallets/NewSubwallet";
import { SubwalletCreated } from "src/screens/subwallets/SubwalletCreated";
import { SubwalletList } from "src/screens/subwallets/SubwalletList";
import Wallet from "src/screens/wallet";
import NodeAlias from "src/screens/wallet/NodeAlias";
import Receive from "src/screens/wallet/Receive";
import Send from "src/screens/wallet/Send";
import SignMessage from "src/screens/wallet/SignMessage";
import WithdrawOnchainFunds from "src/screens/wallet/WithdrawOnchainFunds";
import ReceiveInvoice from "src/screens/wallet/receive/ReceiveInvoice";

import ReceiveOnchain from "src/screens/wallet/receive/ReceiveOnchain";
import ConfirmPayment from "src/screens/wallet/send/ConfirmPayment";
import LnurlPay from "src/screens/wallet/send/LnurlPay";
import Onchain from "src/screens/wallet/send/Onchain";
import OnchainSuccess from "src/screens/wallet/send/OnchainSuccess";
import PaymentSuccess from "src/screens/wallet/send/PaymentSuccess";
import ZeroAmount from "src/screens/wallet/send/ZeroAmount";
import Swap from "src/screens/wallet/swap";
import AutoSwap from "src/screens/wallet/swap/AutoSwap";
import SwapInStatus from "src/screens/wallet/swap/SwapInStatus";
import SwapOutStatus from "src/screens/wallet/swap/SwapOutStatus";

const routes: RouteObject[] = [
  {
    path: "/",
    children: [
      {
        index: true,
        element: <HomeRedirect />,
      },
      {
        element: <AppLayout />,
        handle: { crumb: () => "Home" },
        children: [
          {
            path: "home",
            element: <DefaultRedirect />,
            handle: { crumb: () => "Dashboard" },
            children: [
              {
                index: true,
                element: <Home />,
              },
            ],
          },
      {
        path: "wallet",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Wallet" },
        children: [
          {
            index: true,
            element: <Wallet />,
          },
          {
            path: "swap",
            handle: { crumb: () => "Swap" },
            children: [
              {
                index: true,
                element: <Swap />,
              },
              {
                path: "out/status/:swapId",
                element: <SwapOutStatus />,
              },
              {
                path: "in/status/:swapId",
                element: <SwapInStatus />,
              },
              {
                path: "auto",
                element: <AutoSwap />,
              },
            ],
          },
          {
            path: "receive",
            handle: { crumb: () => "Receive" },
            children: [
              {
                index: true,
                element: <Receive />,
              },
              {
                handle: { crumb: () => "Receive On-chain" },
                path: "onchain",
                element: <ReceiveOnchain />,
              },
              {
                handle: { crumb: () => "Invoice" },
                path: "invoice",
                element: <ReceiveInvoice />,
              },

            ],
          },
          {
            path: "send",
            handle: { crumb: () => "Send" },
            children: [
              {
                index: true,
                element: <Send />,
              },
              {
                path: "onchain",
                element: <Onchain />,
              },
              {
                path: "lnurl-pay",
                element: <LnurlPay />,
              },
              {
                path: "0-amount",
                element: <ZeroAmount />,
              },
              {
                path: "confirm-payment",
                element: <ConfirmPayment />,
              },
              {
                path: "onchain-success",
                element: <OnchainSuccess />,
              },
              {
                path: "success",
                element: <PaymentSuccess />,
              },
            ],
          },
          {
            path: "sign-message",
            element: <SignMessage />,
            handle: { crumb: () => "Sign Message" },
          },
          {
            path: "node-alias",
            element: <NodeAlias />,
            handle: { crumb: () => "Node Alias" },
          },
          {
            path: "withdraw",
            element: <WithdrawOnchainFunds />,
            handle: { crumb: () => "Withdraw On-Chain Balance" },
          },
        ],
      },
      {
        path: "settings",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Settings" },
        children: [
          {
            path: "",
            element: <SettingsLayout />,
            children: [
              {
                index: true,
                element: <Settings />,
              },
              {
                path: "services",
                element: <Services />,
                handle: { crumb: () => "Services" },
              },
              {
                path: "about",
                element: <About />,
                handle: { crumb: () => "About" },
              },
              {
                path: "auto-unlock",
                element: <AutoUnlock />,
                handle: { crumb: () => "Auto Unlock" },
              },
              {
                path: "change-unlock-password",
                element: <ChangeUnlockPassword />,
                handle: { crumb: () => "Unlock Password" },
              },
              {
                path: "backup",
                element: <Backup />,
                handle: { crumb: () => "Backup" },
              },
              {
                path: "node-migrate",
                element: <MigrateNode />,
              },
              {
                path: "developer",
                element: <DeveloperSettings />,
              },
              {
                path: "debug-tools",
                element: <DebugTools />,
              },
            ],
          },
        ],
      },
      {
        path: "apps",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Connections" },
        children: [
          {
            index: true,
            element: <Connections />,
          },
          {
            path: ":id",
            element: <AppDetails />,
          },
          {
            path: "new",
            element: <NewApp />,
            handle: { crumb: () => "New App" },
          },
          {
            path: "cleanup",
            element: <AppsCleanup />,
          },
        ],
      },
      {
        path: "sub-wallets",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Sub-wallets" },

        children: [
          {
            index: true,
            element: <SubwalletList />,
          },
          {
            path: "new",
            element: <NewSubwallet />,
          },
          {
            path: "created",
            element: <SubwalletCreated />,
          },
        ],
      },
      {
        path: "internal-apps",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Connections" },

      },
      {
        path: "appstore",
        element: <DefaultRedirect />,
        handle: { crumb: () => "App Store" },
        children: [
          {
            path: ":appStoreId",
            element: <AppStoreDetail />,
          },
        ],
      },
      {
        path: "channels",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Node" },
        children: [
          {
            index: true,
            element: <Channels />,
          },
          {
            path: "first",
            handle: { crumb: () => "Your First Channel" },
            children: [
              {
                index: true,
                element: <FirstChannel />,
              },
              {
                path: "opening",
                element: <OpeningFirstChannel />,
              },
              {
                path: "opened",
                element: <OpenedFirstChannel />,
              },
            ],
          },
          {
            path: "auto",
            handle: { crumb: () => "New Channel" },
            children: [
              {
                index: true,
                element: <AutoChannel />,
              },
              {
                path: "opening",
                element: <OpeningAutoChannel />,
              },
              {
                path: "opened",
                element: <OpenedAutoChannel />,
              },
            ],
          },
          {
            path: "outgoing",
            element: <IncreaseOutgoingCapacity />,
            handle: { crumb: () => "Open Channel with On-Chain" },
          },

          {
            path: "order",
            element: <CurrentChannelOrder />,
            handle: { crumb: () => "Current Order" },
          },
          {
            path: "lsp-order",
            element: <OrderChannel />,
            handle: { crumb: () => "Increase Inbound Liquidity" },
          },
          {
            path: "onchain/deposit-flokicoin",
            element: <DepositFlokicoin />,
            handle: { crumb: () => "Deposit Flokicoin" },
          },
        ],
      },
      {
        path: "peers",
        element: <DefaultRedirect />,
        handle: { crumb: () => "Peers" },
        children: [
          {
            index: true,
            element: <Peers />,
          },
          {
            path: "new",
            element: <ConnectPeer />,
            handle: { crumb: () => "Connect Peer" },
          },
        ],
      },
      {
        path: "help",
        element: <DefaultRedirect />,
        handle: { crumb: () => "FAQ" },
        children: [
          {
            index: true,
            element: <FAQ />,
          },
        ],
      },
    ],
      },
    ],
  },
  {
    element: <TwoColumnFullScreenLayout />,
    children: [
      {
        path: "start",
        element: (
          <StartRedirect>
            <Start />
          </StartRedirect>
        ),
      },
      {
        path: "unlock",
        element: <Unlock />,
      },
      {
        path: "welcome",
        element: <Welcome />,
      },
      {
        path: "setup",
        element: <SetupRedirect />,
        children: [
          {
            element: <Navigate to="password" replace />,
          },
          {
            path: "password",
            element: <SetupPassword />,
          },
          {
            path: "services",
            element: <SetupServices />,
          },
          {
            path: "security",
            element: <SetupSecurity />,
          },
          {
            path: "node",
            children: [
              {
                index: true,
                element: <LNDForm />,
              },
            ],
          },
          {
            path: "node-restore",
            element: <RestoreNode />,
          },
          {
            path: "finish",
            element: <SetupFinish />,
          },
        ],
      },
    ],
  },
  {
    path: "create-node-migration-file-success",
    element: <CreateNodeMigrationFileSuccess />,
  },
  {
    path: "intro",
    element: <Intro />,
  },
  {
    path: "/*",
    element: <NotFound />,
  },
];

export default routes;
