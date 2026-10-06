package notify

import "time"

// A task is one customer's one channel. Items retain per-cycle deduplication.
type DigestTask struct {
	ID              int64      `json:"id,string" gorm:"primaryKey"`
	CustomerID      int64      `json:"customerId,string"`
	NotifyDate      string     `json:"notifyDate"`
	Channel         string     `json:"channel"`
	Status          string     `json:"status"`
	Count           int        `json:"count"`
	EarliestExpiry  *time.Time `json:"earliestExpiry"`
	ScheduledAt     time.Time  `json:"scheduledAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	Attempts        int        `json:"attempts"`
	Target          string     `json:"target"`
	Error           string     `json:"error"`
	RequestID       string     `json:"requestId"`
	ContentSnapshot string     `json:"contentSnapshot"`
	Version         int        `json:"version"`
}

func (DigestTask) TableName() string { return "system.notification_tasks" }

type DigestItem struct {
	ID          int64     `json:"id,string" gorm:"primaryKey"`
	TaskID      int64     `json:"taskId,string"`
	CustomerID  int64     `json:"customerId,string"`
	InstanceID  int64     `json:"instanceId,string"`
	InstanceNo  string    `json:"instanceNo"`
	ProductName string    `json:"productName"`
	PrimaryIP   string    `json:"primaryIp"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Channel     string    `json:"channel"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason"`
}

func (DigestItem) TableName() string { return "system.notification_task_items" }

type DigestAttempt struct {
	ID         int64     `json:"id,string" gorm:"primaryKey"`
	TaskID     int64     `json:"taskId,string"`
	Status     string    `json:"status"`
	RequestID  string    `json:"requestId"`
	Error      string    `json:"error"`
	AdminID    int64     `json:"adminId,string"`
	OperatorIP string    `json:"operatorIp"`
	Notes      string    `json:"notes"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (DigestAttempt) TableName() string { return "system.notification_task_attempts" }

type DigestQuota struct {
	CustomerID int64  `gorm:"primaryKey"`
	NotifyDate string `gorm:"primaryKey"`
	Channel    string `gorm:"primaryKey"`
	TaskID     int64
}

func (DigestQuota) TableName() string { return "system.notification_daily_quota" }

type DigestSettings struct {
	Enabled         bool  `json:"enabled"`
	DispatchEnabled bool  `json:"dispatchEnabled"`
	AllCustomers    bool  `json:"allCustomers"`
	CustomerID      int64 `json:"customerId,string"`
}
type DigestScan struct {
	ID         string     `json:"id" gorm:"primaryKey"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	NextScanAt *time.Time `json:"nextScanAt"`
	Scanned    int        `json:"scanned"`
	Generated  int        `json:"generated" gorm:"column:generated_count"`
	Error      string     `json:"error"`
}

func (DigestScan) TableName() string { return "system.notification_scan_state" }

func (r *Repository) ensureDigestSchema() []string {
	return []string{
		"CREATE TABLE IF NOT EXISTS system.notification_tasks (id bigint NOT NULL PRIMARY KEY, customer_id bigint NOT NULL, notify_date varchar(10) NOT NULL, channel varchar(16) NOT NULL, status varchar(24) NOT NULL, count int NOT NULL DEFAULT 0, earliest_expiry datetime NULL, scheduled_at datetime NOT NULL, created_at datetime NOT NULL, updated_at datetime NOT NULL, started_at datetime NULL, finished_at datetime NULL, attempts int NOT NULL DEFAULT 0, target varchar(255) NOT NULL DEFAULT '', error varchar(1024) NOT NULL DEFAULT '', request_id varchar(128) NOT NULL DEFAULT '', content_snapshot mediumtext NOT NULL, version int NOT NULL DEFAULT 1, UNIQUE KEY uk_digest_customer_day_channel(customer_id,notify_date,channel), KEY idx_digest_due(status,scheduled_at), KEY idx_digest_customer(customer_id,created_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci",
		"CREATE TABLE IF NOT EXISTS system.notification_task_items (id bigint NOT NULL PRIMARY KEY, task_id bigint NOT NULL, customer_id bigint NOT NULL, instance_id bigint NOT NULL, instance_no varchar(128) NOT NULL, product_name varchar(255) NOT NULL, primary_ip varchar(255) NOT NULL DEFAULT '', expires_at datetime NOT NULL, channel varchar(16) NOT NULL, status varchar(24) NOT NULL DEFAULT 'pending', reason varchar(1024) NOT NULL DEFAULT '', UNIQUE KEY uk_digest_item_cycle(customer_id,instance_id,expires_at,channel), KEY idx_digest_item_task(task_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci",
		"CREATE TABLE IF NOT EXISTS system.notification_task_attempts (id bigint NOT NULL PRIMARY KEY, task_id bigint NOT NULL, status varchar(24) NOT NULL, request_id varchar(128) NOT NULL DEFAULT '', error varchar(1024) NOT NULL DEFAULT '', admin_id bigint NOT NULL DEFAULT 0, operator_ip varchar(128) NOT NULL DEFAULT '', notes varchar(1024) NOT NULL DEFAULT '', created_at datetime NOT NULL, KEY idx_digest_attempt(task_id,created_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci",
		"CREATE TABLE IF NOT EXISTS system.notification_daily_quota (customer_id bigint NOT NULL, notify_date varchar(10) NOT NULL, channel varchar(16) NOT NULL, task_id bigint NOT NULL, PRIMARY KEY(customer_id,notify_date,channel)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci",
		"CREATE TABLE IF NOT EXISTS system.notification_scan_state (id varchar(32) NOT NULL PRIMARY KEY, started_at datetime NULL, finished_at datetime NULL, next_scan_at datetime NULL, scanned int NOT NULL DEFAULT 0, generated_count int NOT NULL DEFAULT 0, error varchar(1024) NOT NULL DEFAULT '') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci",
	}
}
