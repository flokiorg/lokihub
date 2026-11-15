package core

import "github.com/flokiorg/flnd/tlv"

var ExtraFeeTlvType tlv.Type = 65536

func NewExtraFeeRecord(feeMloki *uint64) tlv.Record {
	return tlv.MakePrimitiveRecord(ExtraFeeTlvType, feeMloki)
}
