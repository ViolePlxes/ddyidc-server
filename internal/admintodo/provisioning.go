package admintodo

// status=2 is also used by maintenance operations. Only an unfinished first
// delivery belongs in this queue. An upstream registration date can exist
// before first delivery, so a still-pending order item may corroborate it.
// Keep this predicate shared by the summary, list and history transition.
const provisioningPredicate = `i.status IN (1,2)
 AND LOWER(TRIM(COALESCE(i.power_status,''))) IN ('','pending','creating','provisioning')
 AND (i.activated_at IS NULL OR EXISTS (
   SELECT 1 FROM ` + "`order`.order_items" + ` pi
   WHERE pi.id=i.order_item_id AND pi.order_id=i.order_id AND pi.delivery_status IN (0,1)))
 AND NOT EXISTS (SELECT 1 FROM instance.instance_events pe
   WHERE pe.instance_id=i.id AND (pe.from_status IN (3,4) OR (pe.status=2 AND pe.to_status IN (3,4))))
 AND NOT EXISTS (SELECT 1 FROM ` + "`order`.order_items" + ` di
   WHERE di.id=i.order_item_id AND di.order_id=i.order_id AND di.delivery_status=2)
 AND NOT EXISTS (SELECT 1 FROM ` + "`order`.orders" + ` po WHERE po.id=i.order_id AND po.status=6)`
