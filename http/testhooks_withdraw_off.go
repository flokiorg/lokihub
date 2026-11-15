//go:build !testhooks

package http

import "github.com/labstack/echo/v4"

// registerWithdrawTestHooks: a production build serves no test hooks.
func (httpSvc *HttpService) registerWithdrawTestHooks(*echo.Group) {}
