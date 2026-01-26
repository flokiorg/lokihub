#!/bin/bash

# 1. Receiver-side fixes (JIT Fees & Invoice Amount)
git add lsps/manager/manager.go frontend/src/screens/wallet/receive/ReceiveInvoice.tsx api/lsps.go
git commit -m "fix(lsps2): correct JIT fee calculation and invoice net amount"

# 2. Sender-side transparency (Fee Estimation)
git add lndecodepay/ transactions/transactions_service.go api/transactions.go api/models.go http/http_service.go wails/wails_handlers.go frontend/src/screens/wallet/send/ConfirmPayment.tsx frontend/src/types.ts
git commit -m "feat(sender): add invoice fee estimation and transparency"

# 3. LSPS Hardening (Timeouts & Error Codes)
git add api/lsps.go http/http_service.go lsps/lsps1/ lsps/lsps2/ lsps/lsps5/ lsps/lsps0/
git commit -m "fix(lsps): enforce 5s timeouts and improve error propagation"

# 4. Frontend LSPS Improvements (Order Channel Flow)
git add frontend/src/screens/channels/OrderChannel.tsx frontend/src/hooks/
git commit -m "feat(ui): improve order channel fee visibility and success state"

# 5. Remaining updates (Config, deps, etc)
git add .
git commit -m "chore: update dependencies and configs"
