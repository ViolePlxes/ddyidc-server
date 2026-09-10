package payment

type CreateRequest struct {
	Channel string `json:"channel"`
	Mode    string `json:"mode"`
}

type CreateRechargeRequest struct {
	RechargeID string `json:"rechargeId"`
	Channel    string `json:"channel"`
	Mode       string `json:"mode"`
}

type WechatCheckout struct {
	CodeURL string `json:"codeUrl"`
}

type AlipayCheckout struct {
	PayURL string `json:"payUrl"`
	Mode   string `json:"mode"`
}

type CheckoutResponse struct {
	PaymentID string          `json:"paymentId"`
	PayNo     string          `json:"payNo"`
	Status    string          `json:"status"`
	ExpireAt  string          `json:"expireAt"`
	Wechat    *WechatCheckout `json:"wechat,omitempty"`
	Alipay    *AlipayCheckout `json:"alipay,omitempty"`
}

type StatusResponse struct {
	PayNo      string `json:"payNo"`
	Status     string `json:"status"`
	Channel    string `json:"channel"`
	PaidAmount string `json:"paidAmount"`
	PaidAt     string `json:"paidAt,omitempty"`
}

type QueryRequest struct {
	PayNo string `json:"payNo"`
}

type QueryResponse struct {
	PayNo      string `json:"payNo"`
	Connected  bool   `json:"connected"`
	Found      bool   `json:"found"`
	Status     string `json:"status"`
	Reconciled bool   `json:"reconciled"`
	TradeNo    string `json:"tradeNo,omitempty"`
}

type RefundRequest struct {
	PaymentID string `json:"paymentId"`
	Amount    string `json:"amount"`
	Reason    string `json:"reason"`
}
