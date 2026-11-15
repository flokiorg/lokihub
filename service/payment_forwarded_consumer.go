package service

import (
	"context"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

type paymentForwardedConsumer struct {
	events.EventSubscriber
	db *gorm.DB
}

// When a new app is created, subscribe to it on the relay
func (c *paymentForwardedConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event != "nwc_payment_forwarded" {
		return
	}

	properties, ok := event.Properties.(*lnclient.PaymentForwardedEventProperties)
	if !ok {
		logger.Logger.Error().Interface("event", event).Msg("Failed to cast event.Properties to payment forwarded event properties")
		return
	}
	forward := &db.Forward{
		OutboundAmountForwardedMloki: properties.OutboundAmountForwardedMloki,
		TotalFeeEarnedMloki:          properties.TotalFeeEarnedMloki,
	}
	err := c.db.Create(forward).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to save forward to db")
	}
}
