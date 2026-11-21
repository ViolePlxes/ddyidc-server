package admintodo

// Keep revision inputs identical to the event types that can open this queue.
const manualProductEventTypes = "'create','suspend','terminate','destroy','renew','sync_due_time','unsuspend'"

// manualProductReason prefers the supplier's own failure text and answers the
// question the row is actually about. For a running machine that is the renewal
// that failed; procurement text only wins when no renewal is uncertain. The
// system-written sentences are a last resort so the column is never silently
// empty. delivery_units has no last_error column (see 008_delivery_units.sql).
func manualProductReason(instance string) string {
	return `COALESCE(` +
		renewalText(instance) + `,` +
		procurementText(instance, instance+`.order_id`) + `,` +
		failedEventText(instance) + `,` +
		taskText(instance, instance+`.order_id`) + `,'')`
}

// manualProductReasonSource names the table the reason came from, so the display
// layer can bind the supplier document that belongs to the actual failure.
func manualProductReasonSource(instance string) string {
	return `CASE` +
		` WHEN EXISTS(SELECT 1 FROM instance.instance_renewals r WHERE r.instance_id=` + instance + `.id AND r.state IN ('uncertain','failed') AND COALESCE(r.last_error,'')<>'') THEN 'renewal'` +
		` WHEN EXISTS(SELECT 1 FROM ` + "`order`" + `.unit_procurements p WHERE p.order_id=` + instance + `.order_id AND COALESCE(p.last_error,'')<>'') THEN 'procurement'` +
		` WHEN EXISTS(SELECT 1 FROM instance.instance_events e WHERE e.instance_id=` + instance + `.id AND e.status=3 AND e.event_type IN (` + manualProductEventTypes + `) AND COALESCE(e.error_msg,'')<>'' AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=e.id)) THEN 'event'` +
		` WHEN EXISTS(SELECT 1 FROM system.durable_tasks t WHERE t.kind='post_payment' AND t.status IN ('unknown','failed') AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=CAST(` + instance + `.customer_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=CAST(` + instance + `.order_id AS CHAR)) THEN 'task'` +
		` ELSE '' END`
}

// manualOrderReason applies the same priority to a paid order that has no
// instance yet; the procurement row carries the supplier text.
func manualOrderReason() string {
	return `COALESCE(` + procurementText("o", "o.id") + `,` + taskText("o", "o.id") + `,'')`
}

func manualOrderReasonSource() string {
	return `CASE` +
		` WHEN EXISTS(SELECT 1 FROM ` + "`order`" + `.unit_procurements p WHERE p.order_id=o.id AND COALESCE(p.last_error,'')<>'') THEN 'procurement'` +
		` WHEN EXISTS(SELECT 1 FROM system.durable_tasks t WHERE t.kind='post_payment' AND t.status IN ('unknown','failed') AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=CAST(o.customer_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=CAST(o.id AS CHAR)) THEN 'task'` +
		` ELSE '' END`
}

func renewalText(instance string) string {
	return `(SELECT CONVERT(NULLIF(r.last_error,'') USING utf8mb4) COLLATE utf8mb4_unicode_ci FROM instance.instance_renewals r WHERE r.instance_id=` + instance + `.id AND r.state IN ('uncertain','failed') ORDER BY r.updated_at DESC LIMIT 1)`
}

func procurementText(alias, orderID string) string {
	return `(SELECT CONVERT(NULLIF(p.last_error,'') USING utf8mb4) COLLATE utf8mb4_unicode_ci FROM ` + "`order`" + `.unit_procurements p WHERE p.order_id=` + orderID + ` ORDER BY p.updated_at DESC, p.id DESC LIMIT 1)`
}

func failedEventText(instance string) string {
	return `(SELECT CONVERT(NULLIF(e.error_msg,'') USING utf8mb4) COLLATE utf8mb4_unicode_ci FROM instance.instance_events e WHERE e.instance_id=` + instance + `.id AND e.status=3 AND e.event_type IN (` + manualProductEventTypes + `) AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=e.id) ORDER BY e.created_at DESC LIMIT 1)`
}

func taskText(alias, orderID string) string {
	return `(SELECT CONVERT(NULLIF(t.safe_error,'') USING utf8mb4) COLLATE utf8mb4_unicode_ci FROM system.durable_tasks t WHERE t.kind='post_payment' AND t.status IN ('unknown','failed') AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=CAST(` + alias + `.customer_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=CAST(` + orderID + ` AS CHAR) ORDER BY t.updated_at DESC LIMIT 1)`
}

// Failed delivery reserves an instance ID before creating an instance row.
// Keep these paid purchases visible through their order item until delivery
// creates the actual instance. Reads never requeue procurement or charge again.
//
// amount is the order's paid total: the queue handles paid products, so the
// administrator needs the figure the customer actually paid.
func manualProductProjection() string {
	instance := `SELECT CAST(i.id AS CHAR) id,'instance' source,CAST(i.customer_id AS CHAR) customer_id,i.product_name title,i.instance_no number,'review' status,` +
		manualProductReason("i") + ` reason,` +
		manualProductReasonSource("i") + ` reason_source,` +
		`COALESCE(CAST(o.pay_amount AS CHAR),'') amount,CAST(i.order_id AS CHAR) order_id,CAST(i.id AS CHAR) instance_id,i.created_at,i.updated_at ` +
		`FROM instance.instances i LEFT JOIN ` + "`order`" + `.orders o ON o.id=i.order_id ` +
		`WHERE i.deleted_at IS NULL AND (EXISTS (SELECT 1 FROM ` + "`order`" + `.delivery_units d WHERE d.instance_id=i.id AND d.status IN ('unknown','failed')) OR EXISTS (SELECT 1 FROM instance.instance_renewals r WHERE r.instance_id=i.id AND r.state IN ('uncertain','failed')) OR EXISTS (SELECT 1 FROM instance.instance_upgrades u WHERE u.instance_id=i.id AND u.state IN ('creating','paying','uncertain','verifying')) OR EXISTS (SELECT 1 FROM instance.instance_events e WHERE e.instance_id=i.id AND e.status=3 AND e.event_type IN (` + manualProductEventTypes + `) AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=e.id)))`
	order := `UNION ALL SELECT CAST(oi.id AS CHAR),'order',CAST(o.customer_id AS CHAR),oi.product_name,o.order_no,'review',` +
		manualOrderReason() + `,` + manualOrderReasonSource() + `,COALESCE(CAST(o.pay_amount AS CHAR),''),CAST(o.id AS CHAR),'',COALESCE(o.paid_at,o.created_at),oi.updated_at ` +
		`FROM ` + "`order`" + `.orders o JOIN ` + "`order`" + `.order_items oi ON oi.order_id=o.id ` +
		`WHERE o.status=2 AND NOT EXISTS(SELECT 1 FROM instance.instance_renewals r JOIN instance.instances h ON h.id=r.instance_id AND h.customer_id=r.customer_id WHERE r.order_id=o.id AND r.customer_id=o.customer_id AND h.deleted_at IS NULL) AND NOT EXISTS(SELECT 1 FROM instance.instance_upgrades u JOIN instance.instances h ON h.id=u.instance_id AND h.customer_id=u.customer_id WHERE u.order_id=o.id AND u.customer_id=o.customer_id AND h.deleted_at IS NULL) AND (` +
		` EXISTS(SELECT 1 FROM ` + "`order`" + `.delivery_units d WHERE d.order_item_id=oi.id AND d.order_id=o.id AND d.customer_id=o.customer_id AND d.status IN ('unknown','failed') AND NOT EXISTS(SELECT 1 FROM instance.instances h WHERE h.id=d.instance_id))` +
		` OR (NOT EXISTS(SELECT 1 FROM instance.instances h WHERE h.order_item_id=oi.id) AND (` +
		`  oi.delivery_status=3` +
		`  OR EXISTS(SELECT 1 FROM product.upstream_products p WHERE p.product_id=oi.product_id AND p.auto_setup=0)` +
		`  OR EXISTS(SELECT 1 FROM system.durable_tasks t WHERE t.kind='post_payment' AND t.status IN ('unknown','failed') AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=CAST(o.customer_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=CAST(o.id AS CHAR))` +
		`  )))`
	return instance + order
}
