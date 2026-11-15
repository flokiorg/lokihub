package lightning

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/wire"
)

func NewOutPoint(fundingTxID []byte, index uint32) (*wire.OutPoint, error) {
	var h chainhash.Hash
	err := h.SetBytes(fundingTxID)
	if err != nil {
		log.Printf("h.SetBytes(%x) error: %v", fundingTxID, err)
		return nil, err
	}

	return wire.NewOutPoint(&h, index), nil
}

func NewOutPointFromString(outpoint string) (*wire.OutPoint, error) {
	split := strings.Split(outpoint, ":")
	if len(split) != 2 {
		return nil, fmt.Errorf("invalid outpoint")
	}

	// chainhash.Decode reverses the byte-reversed display-order hex (the
	// convention LND uses for FundingTxidStr / channel_point strings)
	// back into wire order. A plain hex.DecodeString + SetBytes here
	// would silently store the txid byte-reversed, breaking any
	// comparison against outpoints built from raw wire-order bytes
	// (e.g. FundingTxidBytes via NewOutPoint).
	var h chainhash.Hash
	if err := chainhash.Decode(&h, split[0]); err != nil {
		return nil, fmt.Errorf("invalid outpoint")
	}

	outnum, err := strconv.ParseUint(split[1], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid outpoint")
	}

	return wire.NewOutPoint(&h, uint32(outnum)), nil
}
