package circlelife

import (
	"github.com/flokiorg/lokihub/events"
)

// CircleNotificationEvent carries a Circle Wallet notification (NIP-CW
// §Recovering a Lost Connection) to the NIP-47 notifier.
const CircleNotificationEvent = "nwc_circle_notification"

// CircleNotificationProperties is a CircleNotificationEvent's payload: the
// wallet whose current key is told, the notification_type and its body.
type CircleNotificationProperties struct {
	WalletAppID  uint
	Type         string
	Notification map[string]interface{}
}

// Publish sends notices to their wallets' current keys through the NIP-47
// notifier, after the commit that produced them.
func Publish(publisher events.EventPublisher, notices []Notice) {
	if publisher == nil {
		return
	}
	for _, n := range notices {
		publisher.Publish(&events.Event{Event: CircleNotificationEvent, Properties: &CircleNotificationProperties{
			WalletAppID: n.WalletAppID, Type: n.Type, Notification: n.Notification,
		}})
	}
}
