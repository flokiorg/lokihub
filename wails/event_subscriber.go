package wails

import (
	"context"

	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type WailsEventSubscriber struct {
	ctx context.Context
}

func NewWailsEventSubscriber(ctx context.Context) *WailsEventSubscriber {
	return &WailsEventSubscriber{
		ctx: ctx,
	}
}

func (s *WailsEventSubscriber) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if s.ctx == nil {
		return
	}

	logger.Logger.Debug().Str("event", event.Event).Msg("Emitting Wails event")
	runtime.EventsEmit(s.ctx, event.Event, event)
}
