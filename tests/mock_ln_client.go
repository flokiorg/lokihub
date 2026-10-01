package tests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/tv42/zbase32"
)

// for the invoice:
// lnbcrt5u1pjuywzppp5h69dt59cypca2wxu69sw8ga0g39a3yx7dqug5nthrw3rcqgfdu4qdqqcqzzsxqyz5vqsp5gzlpzszyj2k30qmpme7jsfzr24wqlvt9xdmr7ay34lfelz050krs9qyyssq038x07nh8yuv8hdpjh5y8kqp7zcd62ql9na9xh7pla44htjyy02sz23q7qm2tza6ct4ypljk54w9k9qsrsu95usk8ce726ytep6vhhsq9mhf9a
const MockPaymentHash500 = "be8ad5d0b82071d538dcd160e3a3af444bd890de68388a4d771ba23c01096f2a"

const MockInvoice = "lntbs1230n1pnkqautdqyw3jsnp4q09a0z84kg4a2m38zjllw43h953fx5zvqe8qxfgw694ymkq26u8zcpp5yvnh6hsnlnj4xnuh2trzlnunx732dv8ta2wjr75pdfxf6p2vlyassp5hyeg97a3ft5u769kjwsn7p0e85h79pzz8kladmnqhpcypz2uawjs9qyysgqcqpcxq8zals8sq9yeg2pa9eywkgj50cyzxd5elatujuc0c0wh6j9nat5mn34pgk8u9ufpgs99tw9ldlfk42cqlkr48au3lmuh09269prg4qkggh4a8cyqpfl0y6j"
const MockPaymentHash = "23277d5e13fce5534f9752c62fcf9337a2a6b0ebea9d21fa816a4c9d054cf93b" // for the above invoice

const MockZeroAmountInvoice = "lntbs1pnkjfgudqjd3hkueeqv4u8q6tj0ynp4qws83mqzuqptu5kfvxeles7qmyhsj6u2s6zyuft26mcr4tdmcupuupp533y9nwnsaktr9zlvyxmv97ta23faerygh3t9xvsfwytsr28lgggssp5mku3023z3kdxlpx6vrwtfxvvrxpffrquy6veex4ndk7rxhdtslhq9qyysgqcqpcxqxfvltyqva6y7k89jwtcljx399jl6wsq4lkq29vnm3rj4jxmapc6vcs358sx8mtpgh93rdc6ccqpxwwfga59zrla5m55zwzck2y2rsrxumu852sqkvpcm7"
const MockZeroAmountPaymentHash = "8c4859ba70ed96328bec21b6c2f97d5453dc8c88bc56533209711701a8ff4211"

var MockNodeInfo = lnclient.NodeInfo{
	Alias:       "bob",
	Color:       "#3399FF",
	Pubkey:      "123pubkey",
	Network:     "testnet",
	BlockHeight: 12,
	BlockHash:   "123blockhash",
}

var MockLNClientBalances = lnclient.BalancesResponse{
	Lightning: lnclient.LightningBalanceResponse{
		TotalSpendable: 21000,
	},
}

var MockTime = time.Unix(1693876963, 0)
var MockTimeUnix = MockTime.Unix()

var MockLNClientTransactions = []lnclient.Transaction{
	{
		Type:            "incoming",
		Invoice:         MockInvoice,
		Description:     "mock invoice 1",
		DescriptionHash: "hash1",
		Preimage:        "preimage1",
		PaymentHash:     MockPaymentHash,
		Amount:          1000,
		FeesPaid:        50,
		SettledAt:       &MockTimeUnix,
		Metadata: map[string]interface{}{
			"key1": "value1",
			"key2": 42,
		},
	},
	{
		Type:            "incoming",
		Invoice:         MockInvoice,
		Description:     "mock invoice 2",
		DescriptionHash: "hash2",
		Preimage:        "preimage2",
		PaymentHash:     MockPaymentHash,
		Amount:          2000,
		FeesPaid:        75,
		SettledAt:       &MockTimeUnix,
	},
}
var MockLNClientTransaction = &MockLNClientTransactions[0]

var MockLNClientHoldTransaction = &lnclient.Transaction{
	Type:            "incoming",
	Invoice:         "lntb10n1p5zg5p7dqud4hkx6eqdphkcepqd9h8vmmfvdjsnp4qw988hn4lhpu0my4rf0qkraft3wdx5aa0jnjmusgd23z3s0e9qv62pp5yaulxt6x83u4u0x2pck5pyg7fdxhjsd65c9lmu2a9r05qh0cgl6ssp5x57tsnnuc9hr99a9xzg5ylqma5fwvckxa50jqqay5zykqp83h9kq9qyysgqcqpcxqxf92hqqxh0avuskdnuzkk7mxslsdwem3qq3sf79a4ypmx0hax3rupp043yhv97h25vaarj0xrlcg2fdfdhpztsthettskyaylrz6vweztn0twqqv7kxmr",
	Description:     "mock hold invoice",
	DescriptionHash: "",
	Preimage:        "4aa083cad11038359b4f614f3a3d6a8298ae17d5275412bc3eca4f5f4d27f2d4",
	PaymentHash:     "2779f32f463c795e3cca0e2d40911e4b4d7941baa60bfdf15d28df405df847f5",
	Amount:          2000,
}

// SendPaymentSyncCall records one SendPaymentSync invocation's arguments.
//
// It exists because the mock discarded all three — payment request, amount and fee
// limit — and returned a fixed preimage, so no unit test could observe the amount an
// internal transfer actually moved, or whether the route cap a cash_redeem withheld
// ever reached the node. The amountless-same-node defect, which destroyed a slice's
// full amount and answered SUCCESS, lived in exactly that blind spot, and the
// fixtures' "expected" values were this mock's own constants.
//
// Values are copied, not aliased: the caller owns those pointers and may reuse or
// mutate them after the call, which would silently rewrite a recording taken earlier.
type SendPaymentSyncCall struct {
	PayReq        string
	Amount        *uint64
	FeeLimitMloki *uint64
}

// SendKeysendCall records one SendKeysend invocation's arguments.
//
// Same reason SendPaymentSyncCall exists, and the same blind spot: the mock
// discarded all four — amount, destination, custom records and preimage — and
// returned a fixed fee, so no test could observe where a keysend went or how much
// it moved. Audit finding D-QA-3.
//
// CustomRecords is copied rather than aliased: the caller owns that slice.
type SendKeysendCall struct {
	Amount        uint64
	Destination   string
	CustomRecords []lnclient.TLVRecord
	Preimage      string
}

// MakeHoldInvoiceCall records one MakeHoldInvoice invocation's arguments.
//
// PaymentHash is the load-bearing one: a hold invoice is created FOR a hash the
// payer supplied, so passing the wrong one is the defect this records. Audit
// finding D-QA-3.
type MakeHoldInvoiceCall struct {
	Amount          int64
	Description     string
	DescriptionHash string
	Expiry          int64
	PaymentHash     string
}

type MockLn struct {
	PayInvoiceResponses []*lnclient.PayInvoiceResponse
	PayInvoiceErrors    []error
	// MakeInvoiceQueue, when non-empty, is dequeued one entry per MakeInvoice call.
	// Use this to return different invoices (with distinct PaymentHash values) across
	// consecutive calls in the same test, preventing the "already paid" duplicate-hash error.
	MakeInvoiceQueue           []*lnclient.Transaction
	PaymentDelay               *time.Duration
	Pubkey                     string
	MockTransaction            *lnclient.Transaction
	SupportedNotificationTypes *[]string
	// MockLookupInvoiceError, when non-nil, is returned by LookupInvoice instead of MockTransaction.
	// Use this to simulate a node being unreachable during payment reconciliation.
	MockLookupInvoiceError error
	// SendKeysendError, when non-nil, is returned by SendKeysend instead of a success response.
	SendKeysendError error
	// SigningKey makes SignMessage produce a real LND-style zbase32 recoverable
	// signature over the message (compact sig over its double-SHA256). Set Pubkey to
	// this key's compressed hex when a test needs GetPubkey to match the signer.
	//
	// NewMockLn now populates it, because mint provenance is mandatory: every wallet
	// creation signs, and a node that cannot sign refuses the mint outright. A nil key
	// therefore means "this node cannot sign", which is a deliberate failure case
	// rather than a neutral default — a test wanting it clears this explicitly.
	SigningKey *btcec.PrivateKey

	// MakeInvoiceHonoursAmount makes MakeInvoice return an invoice for the amount it
	// was ASKED for, instead of the fixed MockLNClientTransaction constant.
	//
	// Opt-in, deliberately. The default returns a 1000-mloki invoice whatever it was
	// asked for, and many tests assert against that constant, so honouring the amount
	// by default would rewrite their expectations wholesale. It is also the precise
	// shape of the fixture that hid the amountless-redeem bug — tests seeded 1000
	// where a real MakeInvoice(0, …) records 0 — so a test that reasons about amounts
	// should set this, and one that does not care should leave it alone.
	MakeInvoiceHonoursAmount bool

	// callsMu guards the recordings below. lokihub's CI runs `go test -race`, and
	// several suites drive payments from concurrent goroutines against one MockLn, so
	// an unguarded append here would be a data race rather than a flake.
	// HoldInvoiceBindsPreimage makes SettleHoldInvoice refuse a preimage that does
	// not hash to a hold invoice this mock actually issued, the way a node does.
	//
	// Opt-in for the same reason MakeInvoiceHonoursAmount is: suites that drive the
	// settle path without going through MakeHoldInvoice first have no issued invoice
	// to match against, and failing them would be an artefact of the fixture rather
	// than a finding. A test exercising the real preimage->hash binding sets this.
	HoldInvoiceBindsPreimage bool

	callsMu              sync.Mutex
	sendPaymentSyncCalls []SendPaymentSyncCall
	makeInvoiceAmounts   []int64
	sendKeysendCalls     []SendKeysendCall
	makeHoldInvoiceCalls []MakeHoldInvoiceCall
	settleHoldPreimages  []string
	lookupInvoiceHashes  []string
	// issuedHoldHashes is every payment hash MakeHoldInvoice was asked to create an
	// invoice for, which is what HoldInvoiceBindsPreimage checks a settle against.
	issuedHoldHashes map[string]bool
}

// SendPaymentSyncCalls returns every SendPaymentSync argument set so far, in order.
func (mln *MockLn) SendPaymentSyncCalls() []SendPaymentSyncCall {
	mln.callsMu.Lock()
	defer mln.callsMu.Unlock()
	return append([]SendPaymentSyncCall(nil), mln.sendPaymentSyncCalls...)
}

// MakeInvoiceAmounts returns every amount MakeInvoice was asked for, in order —
// including the zeroes, which is the whole point.
func (mln *MockLn) MakeInvoiceAmounts() []int64 {
	mln.callsMu.Lock()
	defer mln.callsMu.Unlock()
	return append([]int64(nil), mln.makeInvoiceAmounts...)
}

// SendKeysendCalls returns every SendKeysend argument set so far, in order.
func (mln *MockLn) SendKeysendCalls() []SendKeysendCall {
	mln.callsMu.Lock()
	defer mln.callsMu.Unlock()
	return append([]SendKeysendCall(nil), mln.sendKeysendCalls...)
}

// MakeHoldInvoiceCalls returns every MakeHoldInvoice argument set so far, in order.
func (mln *MockLn) MakeHoldInvoiceCalls() []MakeHoldInvoiceCall {
	mln.callsMu.Lock()
	defer mln.callsMu.Unlock()
	return append([]MakeHoldInvoiceCall(nil), mln.makeHoldInvoiceCalls...)
}

// SettleHoldPreimages returns every preimage SettleHoldInvoice was handed, in order.
func (mln *MockLn) SettleHoldPreimages() []string {
	mln.callsMu.Lock()
	defer mln.callsMu.Unlock()
	return append([]string(nil), mln.settleHoldPreimages...)
}

// LookupInvoiceHashes returns every payment hash LookupInvoice was asked for, in
// order — the recording that makes a reconciliation's own binding observable.
func (mln *MockLn) LookupInvoiceHashes() []string {
	mln.callsMu.Lock()
	defer mln.callsMu.Unlock()
	return append([]string(nil), mln.lookupInvoiceHashes...)
}

func NewMockLn() (*MockLn, error) {
	// A signing node by default. Minting is impossible without one now, so a mock
	// without a key would make almost every cash test fail for a reason unrelated to
	// what it is testing.
	key, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	return &MockLn{SigningKey: key}, nil
}

func (mln *MockLn) SendPaymentSync(payReq string, amount *uint64, feeLimitMloki *uint64) (*lnclient.PayInvoiceResponse, error) {
	// Recorded first, so a call that goes on to error is still observable — the
	// interesting question is usually what was ASKED for, not what came back.
	call := SendPaymentSyncCall{PayReq: payReq}
	if amount != nil {
		v := *amount
		call.Amount = &v
	}
	if feeLimitMloki != nil {
		v := *feeLimitMloki
		call.FeeLimitMloki = &v
	}
	mln.callsMu.Lock()
	mln.sendPaymentSyncCalls = append(mln.sendPaymentSyncCalls, call)
	mln.callsMu.Unlock()

	// Delay applies before consuming a queued response/error too, so a test can
	// simulate a slow RPC call that ultimately errors (e.g. to race an async
	// settle notification in ahead of the synchronous error return).
	if mln.PaymentDelay != nil {
		time.Sleep(*mln.PaymentDelay)
	}

	if len(mln.PayInvoiceResponses) > 0 {
		response := mln.PayInvoiceResponses[0]
		err := mln.PayInvoiceErrors[0]
		mln.PayInvoiceResponses = mln.PayInvoiceResponses[1:]
		mln.PayInvoiceErrors = mln.PayInvoiceErrors[1:]
		return response, err
	}

	return &lnclient.PayInvoiceResponse{
		Preimage: "123preimage",
	}, nil
}

func (mln *MockLn) SendKeysend(amount uint64, destination string, custom_records []lnclient.TLVRecord, preimage string) (*lnclient.PayKeysendResponse, error) {
	// Recorded before the delay and before any error, so a call that goes on to fail
	// is still observable: what was ASKED for is the interesting part.
	mln.callsMu.Lock()
	mln.sendKeysendCalls = append(mln.sendKeysendCalls, SendKeysendCall{
		Amount:        amount,
		Destination:   destination,
		CustomRecords: append([]lnclient.TLVRecord(nil), custom_records...),
		Preimage:      preimage,
	})
	mln.callsMu.Unlock()
	if mln.PaymentDelay != nil {
		time.Sleep(*mln.PaymentDelay)
	}
	if mln.SendKeysendError != nil {
		return nil, mln.SendKeysendError
	}
	return &lnclient.PayKeysendResponse{
		Fee: 1,
	}, nil
}

func (mln *MockLn) GetInfo(ctx context.Context) (info *lnclient.NodeInfo, err error) {
	return &MockNodeInfo, nil
}

func (mln *MockLn) MakeInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32) (transaction *lnclient.Transaction, err error) {
	mln.callsMu.Lock()
	mln.makeInvoiceAmounts = append(mln.makeInvoiceAmounts, amount)
	mln.callsMu.Unlock()

	if len(mln.MakeInvoiceQueue) > 0 {
		tx := mln.MakeInvoiceQueue[0]
		mln.MakeInvoiceQueue = mln.MakeInvoiceQueue[1:]
		return tx, nil
	}
	if mln.MakeInvoiceHonoursAmount {
		// A COPY. MockLNClientTransaction points into the shared
		// MockLNClientTransactions slice, so setting Amount on it directly would
		// corrupt the fixture for every other test in the package — including ones
		// already running.
		tx := *MockLNClientTransaction
		tx.Amount = amount
		return &tx, nil
	}
	return MockLNClientTransaction, nil
}

func (mln *MockLn) MakeHoldInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, paymentHash string) (transaction *lnclient.Transaction, err error) {
	mln.callsMu.Lock()
	mln.makeHoldInvoiceCalls = append(mln.makeHoldInvoiceCalls, MakeHoldInvoiceCall{
		Amount:          amount,
		Description:     description,
		DescriptionHash: descriptionHash,
		Expiry:          expiry,
		PaymentHash:     paymentHash,
	})
	if paymentHash != "" {
		if mln.issuedHoldHashes == nil {
			mln.issuedHoldHashes = map[string]bool{}
		}
		mln.issuedHoldHashes[paymentHash] = true
	}
	mln.callsMu.Unlock()

	// A hold invoice exists FOR a hash the payer supplied, so returning the fixture's
	// own hash whatever was asked made the one argument that identifies the invoice
	// unobservable — a caller could pass a sibling transaction's hash, or the payment
	// request, and every test stayed green (D-QA-3). The returned invoice now carries
	// the hash it was asked to carry.
	//
	// A COPY, never the shared fixture pointer: handing out MockLNClientHoldTransaction
	// and then writing to it would let one test's hash leak into every other test that
	// holds the same pointer.
	issued := *MockLNClientHoldTransaction
	if amount != 0 {
		issued.Amount = amount
	}
	if paymentHash != "" {
		issued.PaymentHash = paymentHash
		// The fixture's preimage belongs to the fixture's hash. Keeping it next to a
		// different hash would hand tests a preimage that does not hash to it, which
		// is a worse lie than the one being fixed.
		issued.Preimage = ""
	}
	return &issued, nil
}

func (mln *MockLn) SettleHoldInvoice(ctx context.Context, preimage string) (err error) {
	mln.callsMu.Lock()
	mln.settleHoldPreimages = append(mln.settleHoldPreimages, preimage)
	bind := mln.HoldInvoiceBindsPreimage
	issued := len(mln.issuedHoldHashes)
	var known bool
	if preimage != "" {
		if raw, decErr := hex.DecodeString(preimage); decErr == nil {
			sum := sha256.Sum256(raw)
			known = mln.issuedHoldHashes[hex.EncodeToString(sum[:])]
		}
	}
	mln.callsMu.Unlock()

	// Opt-in, and only once an invoice has actually been issued through this mock —
	// otherwise a suite that drives the settle path directly would fail on the
	// fixture's shape rather than on a defect. See HoldInvoiceBindsPreimage.
	if bind && issued > 0 && !known {
		return fmt.Errorf("mock: no accepted hold invoice for preimage %q (sha256 of it matches none of the %d issued by this node)", preimage, issued)
	}
	return nil
}

func (mln *MockLn) CancelHoldInvoice(ctx context.Context, paymentHash string) (err error) {
	return nil
}

func (mln *MockLn) LookupInvoice(ctx context.Context, paymentHash string) (transaction *lnclient.Transaction, err error) {
	mln.callsMu.Lock()
	mln.lookupInvoiceHashes = append(mln.lookupInvoiceHashes, paymentHash)
	mln.callsMu.Unlock()

	if mln.MockLookupInvoiceError != nil {
		return nil, mln.MockLookupInvoiceError
	}
	if mln.MockTransaction != nil {
		// Answer for the hash ASKED FOR, not whatever the fixture holds. The mock used
		// to return MockTransaction unconditionally, so a reconciliation could look up
		// any hash at all — a sibling row's, a hash carried over from a retry, the
		// payment request — and still be handed the invoice it expected, then write
		// that invoice's preimage and FeesPaid onto the wrong transaction. The whole
		// repository stayed green with the lookup pointed at a constant (D-QA-1).
		//
		// Only enforced when the fixture actually names a hash: an empty PaymentHash
		// means the test is not about identity, and failing it would be noise.
		if mln.MockTransaction.PaymentHash != "" && paymentHash != mln.MockTransaction.PaymentHash {
			return nil, fmt.Errorf("mock: no invoice for payment hash %q (this node holds %q)", paymentHash, mln.MockTransaction.PaymentHash)
		}
		return mln.MockTransaction, nil
	}
	return MockLNClientTransaction, nil
}

func (mln *MockLn) ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaid bool, invoiceType string) (invoices []lnclient.Transaction, err error) {
	return MockLNClientTransactions, nil
}
func (mln *MockLn) Shutdown() error {
	return nil
}

func (mln *MockLn) ListChannels(ctx context.Context) (channels []lnclient.Channel, err error) {
	return []lnclient.Channel{}, nil
}
func (mln *MockLn) GetNodeConnectionInfo(ctx context.Context) (nodeConnectionInfo *lnclient.NodeConnectionInfo, err error) {
	return nil, nil
}
func (mln *MockLn) ConnectPeer(ctx context.Context, connectPeerRequest *lnclient.ConnectPeerRequest) error {
	return nil
}
func (mln *MockLn) OpenChannel(ctx context.Context, openChannelRequest *lnclient.OpenChannelRequest) (*lnclient.OpenChannelResponse, error) {
	return nil, nil
}
func (mln *MockLn) CloseChannel(ctx context.Context, closeChannelRequest *lnclient.CloseChannelRequest) (*lnclient.CloseChannelResponse, error) {
	return nil, nil
}
func (mln *MockLn) GetNewOnchainAddress(ctx context.Context) (string, error) {
	return "", nil
}
func (mln *MockLn) GetBalances(ctx context.Context, includeInactiveChannels bool) (*lnclient.BalancesResponse, error) {
	return &MockLNClientBalances, nil
}
func (mln *MockLn) GetOnchainBalance(ctx context.Context) (*lnclient.OnchainBalanceResponse, error) {
	return nil, nil
}
func (mln *MockLn) RedeemOnchainFunds(ctx context.Context, toAddress string, amount uint64, feeRate *uint64, sendAll bool) (txId string, err error) {
	return "", nil
}
func (mln *MockLn) BroadcastTransaction(ctx context.Context, txHex string) error {
	return nil
}
func (mln *MockLn) ResetRouter(key string) error {
	return nil
}
func (mln *MockLn) SendPaymentProbes(ctx context.Context, invoice string) error {
	return nil
}
func (mln *MockLn) SendSpontaneousPaymentProbes(ctx context.Context, amountMloki uint64, nodeId string) error {
	return nil
}
func (mln *MockLn) ListPeers(ctx context.Context) ([]lnclient.PeerDetails, error) {
	return nil, nil
}
func (mln *MockLn) GetLogOutput(ctx context.Context, maxLen int) ([]byte, error) {
	return []byte{}, nil
}
func (mln *MockLn) SignMessage(ctx context.Context, message string) (string, error) {
	if mln.SigningKey == nil {
		return "", nil
	}
	// Mirror flnd's node SignMessage: prepend its context prefix, then a compact
	// recoverable signature over the double-SHA256 of the prefixed message,
	// zbase32-encoded.
	digest := chainhash.DoubleHashB([]byte(lokicash.LNSignedMessagePrefix + message))
	sig := ecdsa.SignCompact(mln.SigningKey, digest, true)
	return zbase32.EncodeToString(sig), nil
}
func (mln *MockLn) GetStorageDir() (string, error) {
	return "", nil
}
func (mln *MockLn) GetNodeStatus(ctx context.Context) (nodeStatus *lnclient.NodeStatus, err error) {
	return &lnclient.NodeStatus{
		IsReady: true,
	}, nil
}
func (mln *MockLn) GetNetworkGraph(ctx context.Context, nodeIds []string) (lnclient.NetworkGraphResponse, error) {
	return nil, nil
}

func (mln *MockLn) UpdateLastWalletSyncRequest() {}

func (mln *MockLn) DisconnectPeer(ctx context.Context, peerId string) error {
	return nil
}

func (mln *MockLn) UpdateChannel(ctx context.Context, updateChannelRequest *lnclient.UpdateChannelRequest) error {
	return nil
}

func (mln *MockLn) GetSupportedNIP47Methods() []string {
	return []string{"pay_invoice", "pay_keysend", "get_balance", "get_budget", "get_info", "make_invoice", "lookup_invoice", "list_transactions", "multi_pay_invoice", "multi_pay_keysend", "sign_message"}
}
func (mln *MockLn) GetSupportedNIP47NotificationTypes() []string {
	if mln.SupportedNotificationTypes != nil {
		return *mln.SupportedNotificationTypes
	}

	return []string{"payment_received", "payment_sent"}
}
func (mln *MockLn) GetPubkey() string {
	if mln.Pubkey != "" {
		return mln.Pubkey
	}

	return "123pubkey"
}

func (mln *MockLn) GetCustomNodeCommandDefinitions() []lnclient.CustomNodeCommandDef {
	return nil
}

func (mln *MockLn) ExecuteCustomNodeCommand(ctx context.Context, command *lnclient.CustomNodeCommandRequest) (*lnclient.CustomNodeCommandResponse, error) {
	return nil, nil
}

func (mln *MockLn) MakeOffer(ctx context.Context, description string) (string, error) {
	return "", errors.New("not supported")
}

func (mln *MockLn) ListOnchainTransactions(ctx context.Context, from, until, limit, offset uint64) ([]lnclient.OnchainTransaction, error) {
	return nil, errors.ErrUnsupported
}

func (mln *MockLn) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	return nil
}

func (mln *MockLn) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	return make(chan lnclient.CustomMessage), make(chan error), nil
}

func (mln *MockLn) SubscribeChannelAcceptor(ctx context.Context) (<-chan lnclient.ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error) {
	return nil, nil, nil
}

func (mln *MockLn) SetNodeAlias(ctx context.Context, alias string) error {
	return nil
}
