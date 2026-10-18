package admintodo

// Alipay personal certification is resolved by the provider, not the manual
// work queue. Company applications and legacy/manual channels still need review.
// Reuse this predicate for count, list and detail; history deliberately omits it.
const manualCertificationPredicate = "r.status=1 AND NOT (r.type=1 AND COALESCE(r.certify_channel,'')='alipay')"
