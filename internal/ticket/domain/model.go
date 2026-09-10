package ticket

import (
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

const (
	StatusPending    int64 = 1
	StatusUserReply  int64 = 2
	StatusReplied    int64 = 3
	StatusClosed     int64 = 4
	StatusProcessing int64 = 5
	ActorClient            = "Client"
	ActorAdmin             = "Admin"
)

type Attachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	Path        string `json:"-"`
	URL         string `json:"url"`
}

type Ticket struct {
	ID               int64           `gorm:"column:id;primaryKey" json:"id,string"`
	TicketNum        string          `gorm:"column:ticket_num" json:"ticketNum"`
	ClientID         int64           `gorm:"column:client_id" json:"clientId,string"`
	Title            string          `gorm:"column:title" json:"title"`
	TicketTypeID     int64           `gorm:"column:ticket_type_id" json:"ticketTypeId,string"`
	Content          string          `gorm:"column:content" json:"content"`
	StatusID         int64           `gorm:"column:status" json:"statusId,string"`
	AttachmentJSON   json.RawMessage `gorm:"column:attachment;type:json" json:"-"`
	LastReplyTime    time.Time       `gorm:"column:last_reply_time" json:"lastReplyTime"`
	PostTime         time.Time       `gorm:"column:post_time" json:"postTime"`
	AdminID          *int64          `gorm:"column:admin_id" json:"adminId,omitempty,string"`
	LastReplyAdminID *int64          `gorm:"column:last_reply_admin_id" json:"lastReplyAdminId,omitempty,string"`
	CreatedAt        time.Time       `gorm:"column:create_time" json:"createdAt"`
	UpdatedAt        time.Time       `gorm:"column:update_time" json:"updatedAt"`
}

func (Ticket) TableName() string { return "ticket.ticket" }
func (v *Ticket) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type HostLink struct {
	ID       int64 `gorm:"column:id;primaryKey"`
	TicketID int64 `gorm:"column:ticket_id"`
	HostID   int64 `gorm:"column:host_id"`
}

func (HostLink) TableName() string { return "ticket.ticket_host_link" }
func (v *HostLink) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Reply struct {
	ID             int64           `gorm:"column:id;primaryKey" json:"id,string"`
	TicketID       int64           `gorm:"column:ticket_id" json:"ticketId,string"`
	Type           string          `gorm:"column:type" json:"type"`
	RelID          int64           `gorm:"column:rel_id" json:"relId,string"`
	Content        string          `gorm:"column:content" json:"content"`
	AttachmentJSON json.RawMessage `gorm:"column:attachment;type:json" json:"-"`
	QuoteReplyID   *int64          `gorm:"column:quote_reply_id" json:"quoteReplyId,omitempty,string"`
	CreatedAt      time.Time       `gorm:"column:create_time" json:"createdAt"`
}

func (Reply) TableName() string { return "ticket.ticket_reply" }
func (v *Reply) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Status struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Name      string    `gorm:"column:name" json:"name"`
	Color     string    `gorm:"column:color" json:"color"`
	Final     bool      `gorm:"column:status" json:"final"`
	IsDefault bool      `gorm:"column:is_default" json:"isDefault"`
	CreatedAt time.Time `gorm:"column:create_time" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:update_time" json:"updatedAt"`
}

func (Status) TableName() string { return "ticket.ticket_status" }
func (v *Status) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Type struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Name      string    `gorm:"column:name" json:"name"`
	CreatedAt time.Time `gorm:"column:create_time" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:update_time" json:"updatedAt"`
}

func (Type) TableName() string { return "ticket.ticket_type" }
func (v *Type) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Note struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	TicketID  int64     `gorm:"column:ticket_id" json:"ticketId,string"`
	Content   string    `gorm:"column:content" json:"content"`
	AdminID   int64     `gorm:"column:admin_id" json:"adminId,string"`
	CreatedAt time.Time `gorm:"column:create_time" json:"createdAt"`
}

func (Note) TableName() string { return "ticket.ticket_notes" }
func (v *Note) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type TypeAdminLink struct {
	TicketTypeID int64 `gorm:"column:ticket_type_id;primaryKey"`
	AdminID      int64 `gorm:"column:admin_id;primaryKey"`
}

func (TypeAdminLink) TableName() string { return "ticket.ticket_type_admin_link" }

type Prereply struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Content   string    `gorm:"column:content" json:"content"`
	Sort      int       `gorm:"column:sort" json:"sort"`
	CreatedAt time.Time `gorm:"column:create_time" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:update_time" json:"updatedAt"`
}

func (Prereply) TableName() string { return "ticket.ticket_prereply" }
func (v *Prereply) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Forward struct {
	ID             int64     `gorm:"column:id;primaryKey" json:"id,string"`
	TicketID       int64     `gorm:"column:ticket_id" json:"ticketId,string"`
	AdminID        int64     `gorm:"column:admin_id" json:"adminId,string"`
	ForwardAdminID *int64    `gorm:"column:forward_admin_id" json:"forwardAdminId,omitempty,string"`
	TicketTypeID   *int64    `gorm:"column:ticket_type_id" json:"ticketTypeId,omitempty,string"`
	Notes          string    `gorm:"column:notes" json:"notes"`
	CreatedAt      time.Time `gorm:"column:create_time" json:"createdAt"`
}

func (Forward) TableName() string { return "ticket.ticket_forward" }
func (v *Forward) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type ReadCursor struct {
	TicketID    int64     `gorm:"column:ticket_id;primaryKey"`
	ActorType   string    `gorm:"column:actor_type;primaryKey"`
	ActorID     int64     `gorm:"column:actor_id;primaryKey"`
	LastReplyID int64     `gorm:"column:last_reply_id"`
	UpdatedAt   time.Time `gorm:"column:update_time"`
}

func (ReadCursor) TableName() string { return "ticket.ticket_reads" }

type Notification struct {
	ID            int64      `gorm:"column:id;primaryKey" json:"id,string"`
	TicketID      int64      `gorm:"column:ticket_id" json:"ticketId,string"`
	RecipientType string     `gorm:"column:recipient_type" json:"recipientType"`
	RecipientID   int64      `gorm:"column:recipient_id" json:"recipientId,string"`
	Kind          string     `gorm:"column:kind" json:"kind"`
	Title         string     `gorm:"column:title" json:"title"`
	Content       string     `gorm:"column:content" json:"content"`
	ReadAt        *time.Time `gorm:"column:read_at" json:"readAt,omitempty"`
	CreatedAt     time.Time  `gorm:"column:create_time" json:"createdAt"`
}

func (Notification) TableName() string { return "ticket.ticket_notifications" }
func (v *Notification) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Log struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	TicketID  int64     `gorm:"column:ticket_id" json:"ticketId,string"`
	ActorType string    `gorm:"column:actor_type" json:"actorType"`
	ActorID   int64     `gorm:"column:actor_id" json:"actorId,string"`
	Action    string    `gorm:"column:action" json:"action"`
	Detail    string    `gorm:"column:detail" json:"detail"`
	CreatedAt time.Time `gorm:"column:create_time" json:"createdAt"`
}

func (Log) TableName() string { return "ticket.ticket_logs" }
func (v *Log) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
