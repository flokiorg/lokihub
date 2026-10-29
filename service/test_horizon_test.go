package service

import (
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"
)

// testHorizon is the default horizon H: how far ahead an envelope's not_after may lie.
var testHorizon = time.Duration(transport.DefaultLimits().MaxRequestAgeSeconds) * time.Second
