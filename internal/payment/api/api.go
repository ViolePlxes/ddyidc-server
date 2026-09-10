package payment

// Package payment reserves the payment domain's cross-domain boundary.
// The current flow is inbound-only: verified payment callbacks invoke the
// order domain through service.PostPaymentCoordinator, so no payment-domain
// implementation type needs to be exported to another domain.
