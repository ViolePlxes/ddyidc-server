package ticket

import (
	dom "ddyidc/internal/ticket/domain"
	"time"
)

type ReplyView struct {
	ID           int64            `json:"id,string"`
	Type         string           `json:"type"`
	RelID        int64            `json:"relId,string"`
	Content      string           `json:"content"`
	Attachments  []dom.Attachment `json:"attachments"`
	QuoteReplyID *int64           `json:"quoteReplyId,omitempty,string"`
	Quote        *QuoteView       `json:"quote,omitempty"`
	CreatedAt    time.Time        `json:"createdAt"`
}
type QuoteView struct {
	ID      int64  `json:"id,string"`
	Type    string `json:"type"`
	Content string `json:"content"`
}
type TicketView struct {
	ID            int64            `json:"id,string"`
	TicketNum     string           `json:"ticketNum"`
	ClientID      int64            `json:"clientId,string"`
	ClientName    string           `json:"clientName,omitempty"`
	Title         string           `json:"title"`
	TicketTypeID  int64            `json:"ticketTypeId,string"`
	TypeName      string           `json:"typeName"`
	Status        dom.Status       `json:"status"`
	Content       string           `json:"content"`
	Attachments   []dom.Attachment `json:"attachments"`
	HostID        *int64           `json:"hostId,omitempty,string"`
	HostName      string           `json:"hostName,omitempty"`
	AdminID       *int64           `json:"adminId,omitempty,string"`
	AdminName     string           `json:"adminName,omitempty"`
	UnreadCount   int64            `json:"unreadCount"`
	LastReplyID   int64            `json:"lastReplyId,string"`
	LastReplyTime time.Time        `json:"lastReplyTime"`
	PostTime      time.Time        `json:"postTime"`
	CreatedAt     time.Time        `json:"createdAt"`
	UpdatedAt     time.Time        `json:"updatedAt"`
}
type DetailView struct {
	Ticket              TicketView  `json:"ticket"`
	Replies             []ReplyView `json:"replies"`
	Notes               []dom.Note  `json:"notes,omitempty"`
	Logs                []dom.Log   `json:"logs,omitempty"`
	CanReply            bool        `json:"canReply"`
	CanUrge             bool        `json:"canUrge"`
	CanClose            bool        `json:"canClose"`
	PollIntervalSeconds int         `json:"pollIntervalSeconds"`
}
type ProductView struct {
	ID        int64      `json:"id,string"`
	Name      string     `json:"name"`
	Status    uint8      `json:"status"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CanReply  bool       `json:"canReply"`
}
type ListResult struct {
	List                []TicketView `json:"list"`
	Total               int64        `json:"total"`
	Page                int          `json:"page"`
	PageSize            int          `json:"pageSize"`
	UnreadCount         int64        `json:"unreadCount"`
	ServerTime          time.Time    `json:"serverTime"`
	PollIntervalSeconds int          `json:"pollIntervalSeconds"`
}
type CreateInput struct {
	Title        string           `json:"title"`
	TicketTypeID int64            `json:"ticketTypeId,string"`
	Content      string           `json:"content"`
	Attachments  []dom.Attachment `json:"attachments"`
	HostID       *int64           `json:"hostId,string"`
}
type ReplyInput struct {
	Content      string           `json:"content"`
	Attachments  []dom.Attachment `json:"attachments"`
	QuoteReplyID *int64           `json:"quoteReplyId,string"`
	Note         string           `json:"note"`
}
type ForwardInput struct {
	ForwardAdminID *int64 `json:"forwardAdminId,string"`
	TicketTypeID   *int64 `json:"ticketTypeId,string"`
	Notes          string `json:"notes"`
}
type ConfigInput struct {
	Name      string `json:"name"`
	Color     string `json:"color"`
	Final     bool   `json:"final"`
	IsDefault bool   `json:"isDefault"`
	Content   string `json:"content"`
	Sort      int    `json:"sort"`
}
