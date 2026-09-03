package server

import "context"

// Notifier sends async notifications about server events.
// A nil Notifier is treated as a no-op by KMSServer.
// Implementations must respect context cancellation for shutdown drain.
type Notifier interface {
	NotifySeal(ctx context.Context, nodeUUID string) error
}
