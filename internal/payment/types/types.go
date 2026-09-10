package payment

type CreateRequest struct {
	Channel string `json:"channel"`
}

type CreateRechargeRequest struct {
	RechargeID string `json:"rechargeId"`
	Channel    string `json:"channel"`
}

type WechatCheckout struct {
	CodeURL string `json:"codeUrl"`
}

type AlipayCheckout struct {
	Form string `json:"form"`
}

type CheckoutResponse struct {
	PaymentID string          `json:"paymentId"`
	Status    string          `json:"status"`
	ExpireAt  string          `json:"expireAt"`
	Wechat    *WechatCheckout `json:"wechat,omitempty"`
	Alipay    *AlipayCheckout `json:"alipay,omitempty"`
}

type StatusResponse struct {
	Status     string `json:"status"`
	Channel    string `json:"channel"`
	PaidAmount string `json:"paidAmount"`
	PaidAt     string `json:"paidAt,omitempty"`
}

type RefundRequest struct {
	PaymentID string `json:"paymentId"`
	Amount    string `json:"amount"`
	Reason    string `json:"reason"`
}
