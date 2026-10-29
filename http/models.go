package http

import "github.com/flokiorg/lokihub/lsps/manager"

type ErrorResponse struct {
	Message string `json:"message"`
}

// TermsErrorResponse refuses a purchase whose live terms are worse than the
// LSP announced, with both, so the user can be asked again (409).
type TermsErrorResponse struct {
	Message string        `json:"message"`
	Terms   manager.Terms `json:"terms"`
}
