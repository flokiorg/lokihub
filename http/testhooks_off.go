//go:build !testhooks

package http

import "github.com/labstack/echo/v4"

// registerTestHooks is a no-op: release builds carry no test hooks.
func (httpSvc *HttpService) registerTestHooks(*echo.Group) {}
