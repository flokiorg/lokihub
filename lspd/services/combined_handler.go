package services

import (
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
)

type CombinedHandler struct {
	handlers []ports.InterceptHandler
}

func NewCombinedHandler(handlers ...ports.InterceptHandler) *CombinedHandler {
	return &CombinedHandler{
		handlers: handlers,
	}
}

func (c *CombinedHandler) Intercept(req core.InterceptRequest) core.InterceptResult {
	for i, handler := range c.handlers {
		res := handler.Intercept(req)
		log.Debug().
			Str("component", string(logging.LogComponentIntercept)).
			Str("req", req.String()).
			Int("handler_index", i).
			Str("result", res.String()).
			Msg("Intercept: Handler processed request")
		if res.Action != core.INTERCEPT_RESUME {
			return res
		}
	}

	return core.InterceptResult{
		Action: core.INTERCEPT_RESUME,
	}
}
