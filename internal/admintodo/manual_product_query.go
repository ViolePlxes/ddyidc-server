package admintodo

// Failed delivery reserves an instance ID before creating an instance row.
// Keep these paid purchases visible through their order item until delivery
// creates the actual instance. Reads never requeue procurement or charge again.
func manualProductProjection() string {
	return `SELECT CAST(i.id AS CHAR) id,'instance' source,CAST(i.customer_id AS CHAR) customer_id,i.product_name title,i.instance_no number,'review' status,'' amount,CAST(i.order_id AS CHAR) order_id,CAST(i.id AS CHAR) instance_id,i.created_at,i.updated_at FROM instance.instances i WHERE i.deleted_at IS NULL AND (EXISTS (SELECT 1 FROM ` + "`order`" + `.delivery_units d WHERE d.instance_id=i.id AND d.status IN ('unknown','failed')) OR EXISTS (SELECT 1 FROM instance.instance_renewals r WHERE r.instance_id=i.id AND r.state IN ('uncertain','failed')) OR EXISTS (SELECT 1 FROM instance.instance_upgrades u WHERE u.instance_id=i.id AND u.state IN ('creating','paying','uncertain','verifying')) OR EXISTS (SELECT 1 FROM instance.instance_events e WHERE e.instance_id=i.id AND e.status=3 AND e.event_type IN ('create','suspend','terminate','destroy','renew','sync_due_time','unsuspend') AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=e.id)))
UNION ALL SELECT CAST(oi.id AS CHAR),'order',CAST(o.customer_id AS CHAR),oi.product_name,o.order_no,'review','',CAST(o.id AS CHAR),'',COALESCE(o.paid_at,o.created_at),oi.updated_at
FROM ` + "`order`" + `.orders o JOIN ` + "`order`" + `.order_items oi ON oi.order_id=o.id
WHERE o.status=2 AND (
 EXISTS(SELECT 1 FROM ` + "`order`" + `.delivery_units d WHERE d.order_item_id=oi.id AND d.order_id=o.id AND d.customer_id=o.customer_id AND d.status IN ('unknown','failed') AND NOT EXISTS(SELECT 1 FROM instance.instances h WHERE h.id=d.instance_id))
 OR (NOT EXISTS(SELECT 1 FROM instance.instances h WHERE h.order_item_id=oi.id) AND (
  oi.delivery_status=3
  OR EXISTS(SELECT 1 FROM product.upstream_products p WHERE p.product_id=oi.product_id AND p.auto_setup=0)
  OR EXISTS(SELECT 1 FROM system.durable_tasks t WHERE t.kind='post_payment' AND t.status IN ('unknown','failed') AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=CAST(o.customer_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=CAST(o.id AS CHAR))
 )))`
}
