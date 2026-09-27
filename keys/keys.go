package keys

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chainutil/hdkeychain"
	btcec "github.com/flokiorg/go-flokicoin/crypto"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/logger"
	"github.com/nbd-wtf/go-nostr"
	"github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
)

type Keys interface {
	Init(cfg config.Config, encryptionKey string) error
	// Wallet Service Nostr pubkey (DEPRECATED)
	GetNostrPublicKey() string
	// Wallet Service Nostr secret key (DEPRECATED)
	GetNostrSecretKey() string
	// Swap rescue key derived from master key using BIP-85
	GetSwapMnemonic() string
	// Derives a BIP32 child key from appKey dedicated for app wallet keys (branch H+1)
	GetAppWalletKey(childIndex uint) (string, error)
	// Derives the hub's private-transport inbox key (branch H+3) — the key clients
	// encrypt envelopes to. Rotatable via index; see GetPrivateTransportKey.
	GetPrivateTransportKey(index uint32) (string, error)
	// Derives the NWC pairing private key for a Cash pending-claim wallet (branch H+2).
	// Cryptographically independent of GetAppWalletKey (different hardened branch index).
	// Never needs to be stored — re-derive at claim time from the app ID.
	GetCashPairingKey(appID uint) (string, error)
	// Derives a child BIP-32 key from the app key (derived from the mnemonic)
	DeriveKey(path []uint32) (*bip32.Key, error)
	// Derives a BIP32 child key from appKey derived child dedicated for swaps
	GetSwapKey(childIndex uint) (*btcec.PrivateKey, error)
}

type keys struct {
	nostrSecretKey string
	nostrPublicKey string
	appKey         *bip32.Key
	swapKey        *hdkeychain.ExtendedKey
	swapMnemonic   string
}

func NewKeys() *keys {
	return &keys{}
}

func (keys *keys) Init(cfg config.Config, encryptionKey string) error {
	nostrSecretKey, _ := cfg.Get("NostrSecretKey", encryptionKey)

	if nostrSecretKey == "" {
		nostrSecretKey = nostr.GeneratePrivateKey()
		err := cfg.SetUpdate("NostrSecretKey", nostrSecretKey, encryptionKey)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save generated nostr secret key")
			return err
		}
	}
	nostrPublicKey, err := nostr.GetPublicKey(nostrSecretKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Error converting nostr privkey to pubkey")
		return err
	}
	keys.nostrSecretKey = nostrSecretKey
	keys.nostrPublicKey = nostrPublicKey

	mnemonic, err := cfg.Get("Mnemonic", encryptionKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to decrypt mnemonic")
		return err
	}

	if mnemonic == "" {
		// for backends that don't use a mnemonic, create one anyway for deriving keys
		entropy, err := bip39.NewEntropy(128)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to generate entropy for mnemonic")
			return err
		}
		mnemonic, err = bip39.NewMnemonic(entropy)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to generate mnemonic")
			return err
		}
		err = cfg.SetUpdate("Mnemonic", mnemonic, encryptionKey)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to save mnemonic")
			return err
		}
	}

	masterKey, err := bip32.NewMasterKey(bip39.NewSeed(mnemonic, ""))
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to create seed from mnemonic")
		return err
	}

	lokihubIndex := uint32(bip32.FirstHardenedChild + 128029 /* 🐝 */)
	appKey, err := masterKey.NewChildKey(lokihubIndex)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to derive app key")
		return err
	}
	keys.appKey = appKey

	swapMnemonic, err := keys.GenerateSwapMnemonic(masterKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to generate swap mnemonic")
		return err
	}
	keys.swapMnemonic = swapMnemonic

	netParams := &chaincfg.MainNetParams
	network := cfg.GetNetwork()
	if network == "testnet" {
		netParams = &chaincfg.TestNet3Params
	}

	swapKey, err := hdkeychain.NewMaster(bip39.NewSeed(swapMnemonic, ""), netParams)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to create seed from swap mnemonic")
		return err
	}
	keys.swapKey = swapKey

	return nil
}

func (keys *keys) GetSwapMnemonic() string {
	return keys.swapMnemonic
}

func (keys *keys) GetNostrPublicKey() string {
	return keys.nostrPublicKey
}

func (keys *keys) GetNostrSecretKey() string {
	return keys.nostrSecretKey
}

func (keys *keys) GetAppWalletKey(appID uint) (string, error) {
	path := []uint32{bip32.FirstHardenedChild + 1, bip32.FirstHardenedChild + uint32(appID)} //nolint:gosec // appID is a small auto-increment DB primary key
	key, err := keys.DeriveKey(path)
	if err != nil {
		return "", err
	}
	childPrivKey, _ := btcec.PrivKeyFromBytes(key.Key)
	return hex.EncodeToString(childPrivKey.Serialize()), nil
}

// GetCashPairingKey derives the NWC client pairing private key for a Cash wallet using
// BIP32 branch H+2, which is cryptographically independent of the wallet key branch H+1.
// This eliminates the need to store the pairing secret in the database — derive it at
// creation time to register the app pubkey, then re-derive it at claim time to build the URI.
func (keys *keys) GetCashPairingKey(appID uint) (string, error) {
	path := []uint32{bip32.FirstHardenedChild + 2, bip32.FirstHardenedChild + uint32(appID)} //nolint:gosec // appID is a small auto-increment DB primary key
	key, err := keys.DeriveKey(path)
	if err != nil {
		return "", err
	}
	childPrivKey, _ := btcec.PrivKeyFromBytes(key.Key)
	privHex := hex.EncodeToString(childPrivKey.Serialize())
	childPrivKey.Zero()
	return privHex, nil
}

// GetPrivateTransportKey derives the hub's private-transport inbox key from BIP32
// branch H+3, independent of the app-wallet (H+1) and Cash-pairing (H+2) branches.
//
// This is the key clients encrypt private-transport envelopes to, and the only key
// that can open them. It is deliberately NOT the Lightning node identity key, even
// though the node key is what a bill's mint signature proves: signing and
// decrypting are different operations, and the node can only do the first. Asking
// the node to perform the ECDH measured 602 µs per inbound event against a 200 µs
// budget — paid on every junk event too, since nothing about a fresh ephemeral
// sender is cacheable — and flnd's DeriveSharedKey returns sha256 of the shared
// point, which NIP-44's HKDF cannot consume at all. Held in-process, the same ECDH
// costs 166 µs. See lnclient/flnd/signrpc_stage0_bench_test.go.
//
// Seed-derived rather than random so it survives a restart without being stored,
// and so rotation is a matter of advancing index rather than key custody. The
// index exists precisely to make rotation possible: a leaked inbox key would
// otherwise expose all future private traffic for every bill in circulation, with
// reissuing every bill as the only remedy.
//
// Clients learn the public half from a replaceable announcement signed by the node
// identity, so the chain bill → node key → announcement → inbox key has no
// assumed link.
func (keys *keys) GetPrivateTransportKey(index uint32) (string, error) {
	path := []uint32{bip32.FirstHardenedChild + 3, bip32.FirstHardenedChild + index}
	key, err := keys.DeriveKey(path)
	if err != nil {
		return "", err
	}
	childPrivKey, _ := btcec.PrivKeyFromBytes(key.Key)
	privHex := hex.EncodeToString(childPrivKey.Serialize())
	childPrivKey.Zero()
	return privHex, nil
}

func (keys *keys) DeriveKey(path []uint32) (*bip32.Key, error) {
	if len(path) == 0 {
		return nil, errors.New("path must have at least one element")
	}
	if keys.appKey == nil {
		return nil, errors.New("app key not set")
	}
	key := keys.appKey
	for _, index := range path {
		var err error
		key, err = key.NewChildKey(index)
		if err != nil {
			return nil, err
		}
	}

	return key, nil
}

func (keys *keys) GetSwapKey(swapID uint) (*btcec.PrivateKey, error) {
	path := []uint32{44, 0, 0, 0, uint32(swapID)} //nolint:gosec // swapID is a small auto-increment DB primary key

	key := keys.swapKey
	for _, index := range path {
		var err error
		key, err = key.Derive(index)
		if err != nil {
			return nil, err
		}
	}

	return key.ECPrivKey()
}

// Taken from https://github.com/e4coder/bip85/blob/main/bip85.go
func (keys *keys) GenerateSwapMnemonic(key *bip32.Key) (string, error) {
	swapIndex := uint32(bip32.FirstHardenedChild + 128260 /* 🔄 */)
	path := []uint32{bip32.FirstHardenedChild + 83696968, bip32.FirstHardenedChild + 39, bip32.FirstHardenedChild + 0, bip32.FirstHardenedChild + 12, swapIndex}

	for _, index := range path {
		var err error
		key, err = key.NewChildKey(index)
		if err != nil {
			return "", err
		}
	}

	hash := hmac.New(sha512.New, []byte("bip-entropy-from-k"))
	_, err := hash.Write(key.Key)
	if err != nil {
		return "", err
	}
	entropy := hash.Sum(nil)
	entropyBIP39 := entropy[:16]
	mnemonic, err := bip39.NewMnemonic(entropyBIP39)
	if err != nil {
		return "", err
	}
	return mnemonic, nil
}
