package logging

// LogComponent defines the source of the log message
type LogComponent string

const (
	LogComponentLSPS0          LogComponent = "LSPS0"
	LogComponentLSPS1          LogComponent = "LSPS1"
	LogComponentLSPS2          LogComponent = "LSPS2"
	LogComponentLSPS5          LogComponent = "LSPS5"
	LogComponentLND            LogComponent = "LND"
	LogComponentIntercept      LogComponent = "Intercept"
	LogComponentSettlement     LogComponent = "Settlement"
	LogComponentChannelSync    LogComponent = "ChannelSync"
	LogComponentNotifications  LogComponent = "Notifications"
	LogComponentForwardsSync   LogComponent = "ForwardsSync"
	LogComponentHistoryStore   LogComponent = "HistoryStore"
	LogComponentOpeningService LogComponent = "OpeningService"
)

// LogAction defines the operation being performed
type LogAction string

const (
	LogActionRequest  LogAction = "request"
	LogActionResponse LogAction = "response"
	LogActionValidate LogAction = "validate"
	LogActionReject   LogAction = "reject"
	LogActionSuccess  LogAction = "success"
	LogActionFailure  LogAction = "failure"
	LogActionRetry    LogAction = "retry"
	LogActionSync     LogAction = "sync"
)
