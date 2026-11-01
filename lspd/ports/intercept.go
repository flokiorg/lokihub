package ports

import "github.com/flokiorg/lokihub/lspd/core"

type InterceptHandler interface {
	Intercept(req core.InterceptRequest) core.InterceptResult
}
