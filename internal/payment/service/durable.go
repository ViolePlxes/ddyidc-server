package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/tasks"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
)

type DurableDispatcher struct{}

// Dispatch remains a compatibility notification. The receipt transaction has
// already committed the durable intention, so no volatile goroutine is needed.
func (*DurableDispatcher) Dispatch(*dom.Payment) {}
func NewDurableDispatcher(ctx context.Context, database *gorm.DB, coordinator PostPaymentCoordinator) *DurableDispatcher {
	go tasks.Run(ctx, database, func(ctx context.Context, task *tasks.Task) error {
		if task.Kind == "customer_notice" {
			var p tasks.NoticePayload
			if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
				return err
			}
			notifier, ok := coordinator.(interface {
				DeliverNotice(context.Context, tasks.NoticePayload) error
			})
			if !ok {
				return errors.New("notification dispatcher unavailable")
			}
			return notifier.DeliverNotice(ctx, p)
		}
		if task.Kind == "upstream_refresh" {
			var p tasks.RefreshPayload
			if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
				return err
			}
			refresh, ok := coordinator.(interface {
				RefreshWebhook(context.Context, tasks.RefreshPayload) error
			})
			if !ok {
				return errors.New("webhook refresh unavailable")
			}
			return refresh.RefreshWebhook(ctx, p)
		}
		if task.Kind != "post_payment" {
			return errors.New("unsupported task")
		}
		var p tasks.PaymentPayload
		if err := json.Unmarshal([]byte(task.Payload), &p); err != nil {
			return err
		}
		switch dom.BizType(p.BusinessType) {
		case "groupbuy_refund_notice", "groupbuy_remind_notice":
			groups, ok := coordinator.(interface {
				NotifyGroupStatus(context.Context, int64, int64, string) error
			})
			if !ok {
				return errors.New("group notification unavailable")
			}
			scene := "groupbuy_refund"
			if p.BusinessType == "groupbuy_remind_notice" {
				scene = "groupbuy_remind"
			}
			return groups.NotifyGroupStatus(ctx, p.CustomerID, p.BusinessID, scene)
		case "groupbuy_notice":
			groups, ok := coordinator.(interface {
				NotifyGroupDelivered(context.Context, int64, int64) error
			})
			if !ok {
				return errors.New("group notification unavailable")
			}
			return groups.NotifyGroupDelivered(ctx, p.CustomerID, p.BusinessID)
		case "groupbuy_plan":
			groups, ok := coordinator.(interface {
				CompleteGroup(context.Context, int64) error
			})
			if !ok {
				return errors.New("group fulfillment unavailable")
			}
			return groups.CompleteGroup(ctx, p.BusinessID)
		case "order_hooks":
			hooks, ok := coordinator.(interface {
				AfterOrderDelivery(context.Context, int64, int64) error
			})
			if !ok {
				return errors.New("delivery hooks unavailable")
			}
			return hooks.AfterOrderDelivery(ctx, p.CustomerID, p.BusinessID)
		case "activation_notice":
			hooks, ok := coordinator.(interface {
				NotifyDeliveredInstance(context.Context, int64, int64) error
			})
			if !ok {
				return errors.New("activation notifier unavailable")
			}
			return hooks.NotifyDeliveredInstance(ctx, p.CustomerID, p.BusinessID)
		case "order_unit":
			delivery, ok := coordinator.(interface {
				ProvisionDeliveryUnit(context.Context, int64, int64, string) error
			})
			if !ok {
				return errors.New("delivery unit handler unavailable")
			}
			return delivery.ProvisionDeliveryUnit(ctx, p.CustomerID, p.BusinessID, p.Channel)
		case dom.BizOrder:
			return coordinator.ProvisionAfterPayment(ctx, p.CustomerID, p.BusinessID, p.Channel)
		case dom.BizRecharge:
			return coordinator.AfterRechargePayment(ctx, p.BusinessID, p.CustomerID, p.AmountFen)
		case dom.BizMarketplace:
			return coordinator.AfterMarketplacePayment(ctx, p.BusinessID, p.CustomerID)
		default:
			return errors.New("unsupported task business")
		}
	})
	return &DurableDispatcher{}
}
