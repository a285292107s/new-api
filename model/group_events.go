package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/registry"
)

// GroupEventTopupSucceeded is the event type published when a top-up completes.
// It is the extension point a future AssignmentPolicy subscribes to; publishing
// it now costs one insert and means the policy can be added later without
// touching any of the six payment functions.
const GroupEventTopupSucceeded = "topup.succeeded"

// TopupSucceededPayload is the event body. It carries only facts the policy
// needs, never the payment credential.
type TopupSucceededPayload struct {
	TopupId    int64   `json:"topup_id"`
	UserId     int     `json:"user_id"`
	Quota      int     `json:"quota"`
	Money      float64 `json:"money"`
	Provider   string  `json:"provider"`
	PaymentM   string  `json:"payment_method"`
	CompletedA int64   `json:"completed_at"`
}

// EmitGroupEvent records a domain event in the outbox. Failures are logged, not
// propagated: an event write must never make a successful payment report as
// failed.
func EmitGroupEvent(eventType string, payload any) {
	if DB == nil {
		return
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		common.SysError(common.LogText("failed to encode group event %s: %s", eventType, err.Error()))
		return
	}
	event := registry.GroupEvent{
		EventType: eventType,
		Payload:   string(encoded),
		CreatedAt: common.GetTimestamp(),
	}
	if err := DB.Create(&event).Error; err != nil {
		common.SysError(common.LogText("failed to persist group event %s: %s", eventType, err.Error()))
	}
}

// ConsumePendingGroupEvents returns unprocessed events of one type. A future
// policy worker calls this in a loop; the current code only records, so the
// table stays a complete audit of top-ups that could drive assignments.
func ConsumePendingGroupEvents(ctx context.Context, eventType string, limit int) ([]registry.GroupEvent, error) {
	var events []registry.GroupEvent
	err := DB.WithContext(ctx).
		Where("event_type = ? AND published_at = 0", eventType).
		Order("id asc").Limit(limit).Find(&events).Error
	return events, err
}
