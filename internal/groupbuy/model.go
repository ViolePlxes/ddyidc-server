package groupbuy

import (
	"encoding/json"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

const (
	activityTable = "system.groupbuy_activities"
	recordTable   = "system.groupbuy_records"
	settingsTable = "system.groupbuy_settings"
)

type Activity struct {
	ID                   int64           `gorm:"column:id;primaryKey" json:"id,string"`
	ProductID            int64           `gorm:"column:product_id" json:"productId,string"`
	ProductName          string          `gorm:"column:product_name" json:"productName"`
	GroupID              int64           `gorm:"column:group_id" json:"groupId,string"`
	GroupName            string          `gorm:"column:group_name" json:"groupName"`
	BillingCycle         string          `gorm:"column:billing_cycle" json:"billingCycle"`
	BillingCycleName     string          `gorm:"column:billing_cycle_name" json:"billingCycleName"`
	ConfigOptions        json.RawMessage `gorm:"column:config_options;type:json" json:"configOptions"`
	ConfigDescription    string          `gorm:"column:config_description" json:"configDescription"`
	OriginalPrice        int64           `gorm:"column:original_price" json:"originalPrice"`
	PriceMode            uint8           `gorm:"column:price_mode" json:"priceMode"`
	TwoPrice             *int64          `gorm:"column:two_price" json:"twoPrice,omitempty"`
	TwoDiscount          *int            `gorm:"column:two_discount" json:"twoDiscount,omitempty"`
	ThreePrice           *int64          `gorm:"column:three_price" json:"threePrice,omitempty"`
	ThreeDiscount        *int            `gorm:"column:three_discount" json:"threeDiscount,omitempty"`
	FivePrice            *int64          `gorm:"column:five_price" json:"fivePrice,omitempty"`
	FiveDiscount         *int            `gorm:"column:five_discount" json:"fiveDiscount,omitempty"`
	TwoLeaderPrice       *int64          `gorm:"column:two_leader_price" json:"twoLeaderPrice,omitempty"`
	TwoLeaderDiscount    *int            `gorm:"column:two_leader_discount" json:"twoLeaderDiscount,omitempty"`
	ThreeLeaderPrice     *int64          `gorm:"column:three_leader_price" json:"threeLeaderPrice,omitempty"`
	ThreeLeaderDiscount  *int            `gorm:"column:three_leader_discount" json:"threeLeaderDiscount,omitempty"`
	FiveLeaderPrice      *int64          `gorm:"column:five_leader_price" json:"fiveLeaderPrice,omitempty"`
	FiveLeaderDiscount   *int            `gorm:"column:five_leader_discount" json:"fiveLeaderDiscount,omitempty"`
	RenewType            uint8           `gorm:"column:renew_type" json:"renewType"`
	TimeoutDays          int             `gorm:"column:timeout_days" json:"timeoutDays"`
	EndTime              time.Time       `gorm:"column:end_time" json:"endTime"`
	Status               uint8           `gorm:"column:status" json:"status"`
	Sort                 int             `gorm:"column:sort" json:"order"`
	IsPublicGroupEnabled bool            `gorm:"column:is_public_group_enabled" json:"isPublicGroupEnabled"`
	ForcePublic          bool            `gorm:"column:force_public" json:"forcePublic"`
	Slogan               string          `gorm:"column:slogan" json:"slogan"`
	RequireCertification bool            `gorm:"column:require_certification" json:"requireCertification"`
	PurchaseLimit        int             `gorm:"column:purchase_limit" json:"purchaseLimit"`
	MemberRestrictMode   uint8           `gorm:"column:member_restrict_mode" json:"memberRestrictMode"`
	HideForOldUser       bool            `gorm:"column:hide_for_old_user" json:"hideForOldUser"`
	CreatedAt            time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt            time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (Activity) TableName() string { return activityTable }
func (v *Activity) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Record struct {
	ID                  int64      `gorm:"column:id;primaryKey" json:"id,string"`
	ActivityID          int64      `gorm:"column:activity_id" json:"activityId,string"`
	ClientID            int64      `gorm:"column:client_id" json:"clientId,string"`
	GroupType           string     `gorm:"column:group_type" json:"groupType"`
	IsLeader            bool       `gorm:"column:is_leader" json:"isLeader"`
	LeaderID            int64      `gorm:"column:leader_id" json:"leaderId,string"`
	CurrentNum          int        `gorm:"column:current_num" json:"currentNum"`
	NeedNum             int        `gorm:"column:need_num" json:"needNum"`
	PayAmount           int64      `gorm:"column:pay_amount" json:"payAmount"`
	GroupPrice          int64      `gorm:"column:group_price" json:"groupPrice"`
	LevelDiscountAmount int64      `gorm:"column:level_discount_amount" json:"levelDiscountAmount"`
	OrderID             *int64     `gorm:"column:order_id" json:"orderId,string,omitempty"`
	HostID              *int64     `gorm:"column:host_id" json:"hostId,string,omitempty"`
	InviteCode          string     `gorm:"column:invite_code" json:"inviteCode"`
	IsPublic            bool       `gorm:"column:is_public" json:"isPublic"`
	ForceClosePublic    bool       `gorm:"column:force_close_public" json:"forceClosePublic"`
	Status              uint8      `gorm:"column:status" json:"status"`
	TimeoutTime         time.Time  `gorm:"column:timeout_time" json:"timeoutTime"`
	SuccessTime         *time.Time `gorm:"column:success_time" json:"successTime,omitempty"`
	IsNotice            bool       `gorm:"column:is_notice" json:"isNotice"`
	RecommendIdentify   string     `gorm:"column:recommend_identify" json:"recommendIdentify,omitempty"`
	RecommendClientID   *int64     `gorm:"column:recommend_client_id" json:"recommendClientId,string,omitempty"`
	CreatedAt           time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt           time.Time  `gorm:"column:updated_at" json:"updatedAt"`
	Activity            *Activity  `gorm:"foreignKey:ActivityID;references:ID" json:"activity,omitempty"`
	MemberName          string     `gorm:"-" json:"memberName,omitempty"`
	MemberPhone         string     `gorm:"-" json:"memberPhone,omitempty"`
}

func (Record) TableName() string { return recordTable }
func (v *Record) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Settings struct {
	ID                        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	ClientLevelDiscountEnable bool      `gorm:"column:client_level_discount_enable" json:"clientLevelDiscountEnable"`
	TimeoutRemindHours        int       `gorm:"column:timeout_remind_hours" json:"timeoutRemindHours"`
	ShowMemberName            bool      `gorm:"column:show_member_name" json:"showMemberName"`
	ShowMemberPhone           bool      `gorm:"column:show_member_phone" json:"showMemberPhone"`
	NewUserGroupLimit         int       `gorm:"column:new_user_group_limit" json:"newUserGroupLimit"`
	CreatedAt                 time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt                 time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (Settings) TableName() string { return settingsTable }

type ActivityView struct {
	Activity
	ActiveGroups  int64 `json:"activeGroups"`
	JoinedMembers int64 `json:"joinedMembers"`
}

type GroupDetail struct {
	Leader   Record   `json:"leader"`
	Members  []Record `json:"members"`
	Activity Activity `json:"activity"`
}
