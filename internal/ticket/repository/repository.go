package ticket

import (
	"context"
	schemamode "ddyidc/internal/shared/schema"
	dom "ddyidc/internal/ticket/domain"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

var ErrClosed = errors.New("已关闭工单不能处理，请先重新打开")

func lockOpenTicket(tx *gorm.DB, id int64) error {
	var ticket dom.Ticket
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ticket, id).Error; err != nil {
		return err
	}
	if ticket.StatusID == dom.StatusClosed {
		return ErrClosed
	}
	return nil
}

func NewRepository(database *gorm.DB) *Repository { return &Repository{db: database} }

func (r *Repository) EnsureSchema(ctx context.Context) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	statements := []string{
		"CREATE SCHEMA IF NOT EXISTS ticket DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		`CREATE TABLE IF NOT EXISTS ticket.ticket_status (id bigint unsigned NOT NULL, name varchar(32) NOT NULL, color varchar(16) NOT NULL, status tinyint(1) NOT NULL DEFAULT 0, is_default tinyint(1) NOT NULL DEFAULT 0, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, update_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_default(is_default)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_type (id bigint unsigned NOT NULL, name varchar(64) NOT NULL, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, update_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY(id), UNIQUE KEY uk_name(name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket (id bigint unsigned NOT NULL, ticket_num varchar(16) NOT NULL, client_id bigint unsigned NOT NULL, title varchar(128) NOT NULL, ticket_type_id bigint unsigned NOT NULL, content text NOT NULL, status bigint unsigned NOT NULL, attachment json DEFAULT NULL, last_reply_time datetime NOT NULL, post_time datetime NOT NULL, admin_id bigint unsigned DEFAULT NULL, last_reply_admin_id bigint unsigned DEFAULT NULL, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, update_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY(id), UNIQUE KEY uk_ticket_num(ticket_num), KEY idx_client_status(client_id,status), KEY idx_admin_status(admin_id,status), KEY idx_update(update_time)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_host_link (id bigint unsigned NOT NULL, ticket_id bigint unsigned NOT NULL, host_id bigint unsigned NOT NULL, PRIMARY KEY(id), UNIQUE KEY uk_ticket_host(ticket_id,host_id), KEY idx_host(host_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_reply (id bigint unsigned NOT NULL, ticket_id bigint unsigned NOT NULL, type varchar(16) NOT NULL, rel_id bigint unsigned NOT NULL, content text NOT NULL, attachment json DEFAULT NULL, quote_reply_id bigint unsigned DEFAULT NULL, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_ticket_id(ticket_id,id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_notes (id bigint unsigned NOT NULL, ticket_id bigint unsigned NOT NULL, content text NOT NULL, admin_id bigint unsigned NOT NULL, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_ticket(ticket_id,create_time)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_type_admin_link (ticket_type_id bigint unsigned NOT NULL, admin_id bigint unsigned NOT NULL, PRIMARY KEY(ticket_type_id,admin_id), KEY idx_admin(admin_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_prereply (id bigint unsigned NOT NULL, content text NOT NULL, sort int NOT NULL DEFAULT 0, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, update_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_sort(sort,id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_forward (id bigint unsigned NOT NULL, ticket_id bigint unsigned NOT NULL, admin_id bigint unsigned NOT NULL, forward_admin_id bigint unsigned DEFAULT NULL, ticket_type_id bigint unsigned DEFAULT NULL, notes varchar(255) NOT NULL DEFAULT '', create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_ticket(ticket_id,create_time)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_reads (ticket_id bigint unsigned NOT NULL, actor_type varchar(16) NOT NULL, actor_id bigint unsigned NOT NULL, last_reply_id bigint unsigned NOT NULL DEFAULT 0, update_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY(ticket_id,actor_type,actor_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_notifications (id bigint unsigned NOT NULL, ticket_id bigint unsigned NOT NULL, recipient_type varchar(16) NOT NULL, recipient_id bigint unsigned NOT NULL, kind varchar(32) NOT NULL, title varchar(128) NOT NULL, content varchar(255) NOT NULL, read_at datetime DEFAULT NULL, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_recipient(recipient_type,recipient_id,read_at,create_time), KEY idx_ticket(ticket_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ticket.ticket_logs (id bigint unsigned NOT NULL, ticket_id bigint unsigned NOT NULL, actor_type varchar(16) NOT NULL, actor_id bigint unsigned NOT NULL, action varchar(32) NOT NULL, detail varchar(255) NOT NULL, create_time datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(id), KEY idx_ticket(ticket_id,create_time)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, statement := range statements {
		if err := r.db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("ticket schema: %w", err)
		}
	}
	defaults := []dom.Status{
		{ID: 1, Name: "待接单", Color: "#779500", Final: false, IsDefault: true},
		{ID: 2, Name: "用户已回复", Color: "#ff6600", Final: false},
		{ID: 3, Name: "已回复", Color: "#000000", Final: true},
		{ID: 4, Name: "已关闭", Color: "#888888", Final: true},
		{ID: 5, Name: "处理中", Color: "#cc0000", Final: false},
	}
	for _, status := range defaults {
		if err := r.db.WithContext(ctx).Table(status.TableName()).Clauses(clause.OnConflict{DoNothing: true}).Create(&status).Error; err != nil {
			return err
		}
	}
	for _, value := range []dom.Type{{ID: 1, Name: "技术支持"}, {ID: 2, Name: "财务问题"}, {ID: 3, Name: "售前咨询"}} {
		if err := r.db.WithContext(ctx).Table(value.TableName()).Clauses(clause.OnConflict{DoNothing: true}).Create(&value).Error; err != nil {
			return err
		}
	}
	return r.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, 'ticket.poll_interval_seconds', '30', '工单增量轮询间隔（秒）')", time.Now().UnixNano()).Error
}

func (r *Repository) Transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}
func (r *Repository) CreateTicket(tx *gorm.DB, ticket *dom.Ticket, reply *dom.Reply, hostID *int64, log *dom.Log) error {
	if err := tx.Table(ticket.TableName()).Create(ticket).Error; err != nil {
		return err
	}
	canonical, err := r.bindUploads(tx, ticket.AttachmentJSON, dom.ActorClient, ticket.ClientID, ticket.ID)
	if err != nil {
		return err
	}
	ticket.AttachmentJSON = canonical
	reply.AttachmentJSON = canonical
	if err := tx.Table(ticket.TableName()).Where("id=?", ticket.ID).Update("attachment", canonical).Error; err != nil {
		return err
	}
	reply.TicketID = ticket.ID
	if err := tx.Table(reply.TableName()).Create(reply).Error; err != nil {
		return err
	}
	if hostID != nil {
		link := dom.HostLink{TicketID: ticket.ID, HostID: *hostID}
		if err := tx.Table(link.TableName()).Create(&link).Error; err != nil {
			return err
		}
	}
	log.TicketID = ticket.ID
	return tx.Table(log.TableName()).Create(log).Error
}

func (r *Repository) FindTicket(ctx context.Context, id int64) (*dom.Ticket, error) {
	var v dom.Ticket
	err := r.db.WithContext(ctx).Table(v.TableName()).Where("id = ?", id).First(&v).Error
	return &v, err
}
func (r *Repository) FindTicketByNumber(ctx context.Context, number string) (*dom.Ticket, error) {
	var v dom.Ticket
	err := r.db.WithContext(ctx).Table(v.TableName()).Where("ticket_num = ?", number).First(&v).Error
	return &v, err
}

func (r *Repository) ListTickets(ctx context.Context, clientID *int64, page, pageSize int, statusID, typeID *int64, keyword, since string) ([]dom.Ticket, int64, error) {
	q := r.db.WithContext(ctx).Table((dom.Ticket{}).TableName())
	if clientID != nil {
		q = q.Where("client_id = ?", *clientID)
	}
	if statusID != nil {
		q = q.Where("status = ?", *statusID)
	}
	if typeID != nil {
		q = q.Where("ticket_type_id = ?", *typeID)
	}
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		q = q.Where("ticket_num LIKE ? OR title LIKE ?", like, like)
	}
	if strings.TrimSpace(since) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, since); err == nil {
			q = q.Where("update_time > ?", parsed)
		}
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []dom.Ticket
	order := "CASE WHEN status = 1 THEN 0 ELSE 1 END, post_time DESC, update_time DESC"
	if clientID != nil {
		order = "update_time DESC"
	}
	err := q.Order(order).Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

func (r *Repository) FindType(ctx context.Context, id int64) (*dom.Type, error) {
	var v dom.Type
	err := r.db.WithContext(ctx).Table(v.TableName()).Where("id = ?", id).First(&v).Error
	return &v, err
}
func (r *Repository) FindStatus(ctx context.Context, id int64) (*dom.Status, error) {
	var v dom.Status
	err := r.db.WithContext(ctx).Table(v.TableName()).Where("id = ?", id).First(&v).Error
	return &v, err
}
func (r *Repository) ListTypes(ctx context.Context) ([]dom.Type, error) {
	var out []dom.Type
	err := r.db.WithContext(ctx).Table((dom.Type{}).TableName()).Order("create_time ASC").Find(&out).Error
	return out, err
}
func (r *Repository) ListStatuses(ctx context.Context) ([]dom.Status, error) {
	var out []dom.Status
	err := r.db.WithContext(ctx).Table((dom.Status{}).TableName()).Order("id ASC").Find(&out).Error
	return out, err
}

type Meta struct {
	TypeName   string
	ClientName string
	AdminName  string
	HostID     *int64
	HostName   string
}

func (r *Repository) TicketMeta(ctx context.Context, v *dom.Ticket) (Meta, error) {
	var out Meta
	r.db.WithContext(ctx).Table("ticket.ticket_type").Select("name").Where("id = ?", v.TicketTypeID).Scan(&out.TypeName)
	r.db.WithContext(ctx).Table("customer.customers").Select("COALESCE(nickname, username)").Where("id = ?", v.ClientID).Scan(&out.ClientName)
	if v.AdminID != nil {
		r.db.WithContext(ctx).Table("system.admins").Select("real_name").Where("id = ?", *v.AdminID).Scan(&out.AdminName)
	}
	var host struct {
		ID   int64
		Name string
	}
	r.db.WithContext(ctx).Table("ticket.ticket_host_link AS l").Select("i.id, i.product_name AS name").Joins("JOIN instance.instances AS i ON i.id = l.host_id").Where("l.ticket_id = ?", v.ID).Limit(1).Scan(&host)
	if host.ID != 0 {
		out.HostID = &host.ID
		out.HostName = host.Name
	}
	return out, nil
}

func (r *Repository) ListReplies(ctx context.Context, ticketID, since int64) ([]dom.Reply, error) {
	var out []dom.Reply
	err := r.db.WithContext(ctx).Table((dom.Reply{}).TableName()).Where("ticket_id = ? AND id > ?", ticketID, since).Order("id ASC").Find(&out).Error
	return out, err
}
func (r *Repository) FindReply(ctx context.Context, ticketID, id int64) (*dom.Reply, error) {
	var v dom.Reply
	err := r.db.WithContext(ctx).Table(v.TableName()).Where("ticket_id = ? AND id = ?", ticketID, id).First(&v).Error
	return &v, err
}
func (r *Repository) LastReplyID(ctx context.Context, ticketID int64) int64 {
	var id int64
	r.db.WithContext(ctx).Table((dom.Reply{}).TableName()).Where("ticket_id = ?", ticketID).Select("COALESCE(MAX(id),0)").Scan(&id)
	return id
}
func (r *Repository) UnreadCount(ctx context.Context, ticketID int64, actorType string, actorID int64) int64 {
	var count int64
	r.db.WithContext(ctx).Table("ticket.ticket_reply AS r").Joins("LEFT JOIN ticket.ticket_reads AS rd ON rd.ticket_id=r.ticket_id AND rd.actor_type=? AND rd.actor_id=?", actorType, actorID).Where("r.ticket_id=? AND r.type<>? AND r.id>COALESCE(rd.last_reply_id,0)", ticketID, actorType).Count(&count)
	return count
}
func (r *Repository) MarkRead(ctx context.Context, ticketID int64, actorType string, actorID, lastReplyID int64) error {
	v := dom.ReadCursor{TicketID: ticketID, ActorType: actorType, ActorID: actorID, LastReplyID: lastReplyID, UpdatedAt: time.Now()}
	return r.db.WithContext(ctx).Table(v.TableName()).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "ticket_id"}, {Name: "actor_type"}, {Name: "actor_id"}}, DoUpdates: clause.Assignments(map[string]any{"last_reply_id": lastReplyID, "update_time": time.Now()})}).Create(&v).Error
}

func (r *Repository) CreateReply(ctx context.Context, reply *dom.Reply, ticketFields map[string]any, log *dom.Log, note *dom.Note) error {
	return r.Transaction(ctx, func(tx *gorm.DB) error {
		if err := lockOpenTicket(tx, reply.TicketID); err != nil {
			return err
		}
		canonical, err := r.bindUploads(tx, reply.AttachmentJSON, reply.Type, reply.RelID, reply.TicketID)
		if err != nil {
			return err
		}
		reply.AttachmentJSON = canonical
		if err := tx.Table(reply.TableName()).Create(reply).Error; err != nil {
			return err
		}
		ticketFields["update_time"] = time.Now()
		if err := tx.Table((dom.Ticket{}).TableName()).Where("id = ?", reply.TicketID).Updates(ticketFields).Error; err != nil {
			return err
		}
		if note != nil {
			if err := tx.Table(note.TableName()).Create(note).Error; err != nil {
				return err
			}
		}
		return tx.Table(log.TableName()).Create(log).Error
	})
}
func (r *Repository) UpdateTicket(ctx context.Context, id int64, fields map[string]any, log *dom.Log) error {
	return r.Transaction(ctx, func(tx *gorm.DB) error {
		if log.Action != "close" {
			if err := lockOpenTicket(tx, id); err != nil {
				return err
			}
		}
		fields["update_time"] = time.Now()
		if err := tx.Table((dom.Ticket{}).TableName()).Where("id = ?", id).Updates(fields).Error; err != nil {
			return err
		}
		return tx.Table(log.TableName()).Create(log).Error
	})
}

type ProductRow struct {
	ID        int64
	Name      string
	Status    uint8
	ExpiresAt *time.Time
	DeletedAt *time.Time
}

func (r *Repository) ListClientProducts(ctx context.Context, clientID int64) ([]ProductRow, error) {
	var out []ProductRow
	err := r.db.WithContext(ctx).Table("instance.instances").Select("id, product_name AS name, status, expires_at, deleted_at").Where("customer_id = ? AND deleted_at IS NULL", clientID).Order("created_at DESC").Find(&out).Error
	return out, err
}
func (r *Repository) CheckHost(ctx context.Context, clientID, hostID int64) (ProductRow, error) {
	var v ProductRow
	err := r.db.WithContext(ctx).Table("instance.instances").Select("id, product_name AS name, status, expires_at, deleted_at").Where("id = ? AND customer_id = ?", hostID, clientID).First(&v).Error
	return v, err
}
func (r *Repository) LinkedHost(ctx context.Context, ticketID int64) (ProductRow, error) {
	var v ProductRow
	err := r.db.WithContext(ctx).Table("ticket.ticket_host_link AS l").Select("i.id, i.product_name AS name, i.status, i.expires_at, i.deleted_at").Joins("JOIN instance.instances AS i ON i.id=l.host_id").Where("l.ticket_id=?", ticketID).First(&v).Error
	return v, err
}

func (r *Repository) ActiveAdminIDs(ctx context.Context, typeID int64, assignee *int64) ([]int64, error) {
	if assignee != nil {
		return []int64{*assignee}, nil
	}
	var ids []int64
	err := r.db.WithContext(ctx).Table("system.admins AS a").Select("a.id").Joins("LEFT JOIN ticket.ticket_type_admin_link AS l ON l.admin_id=a.id AND l.ticket_type_id=?", typeID).Where("a.status=1").Where("l.admin_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM ticket.ticket_type_admin_link x WHERE x.ticket_type_id=?)", typeID).Scan(&ids).Error
	return ids, err
}
func (r *Repository) CreateNotifications(ctx context.Context, items []dom.Notification) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Table((dom.Notification{}).TableName()).Create(&items).Error
}
func (r *Repository) ListNotifications(ctx context.Context, actorType string, actorID int64, limit int) ([]dom.Notification, int64, error) {
	var out []dom.Notification
	var unread int64
	q := r.db.WithContext(ctx).Table((dom.Notification{}).TableName()).Where("recipient_type=? AND recipient_id=?", actorType, actorID)
	if err := q.Where("read_at IS NULL").Count(&unread).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("create_time DESC").Limit(limit).Find(&out).Error
	return out, unread, err
}
func (r *Repository) MarkTicketNotificationsRead(ctx context.Context, ticketID int64, actorType string, actorID int64) {
	r.db.WithContext(ctx).Table((dom.Notification{}).TableName()).Where("ticket_id=? AND recipient_type=? AND recipient_id=? AND read_at IS NULL", ticketID, actorType, actorID).Update("read_at", time.Now())
}

func (r *Repository) ListNotes(ctx context.Context, ticketID int64) ([]dom.Note, error) {
	var out []dom.Note
	err := r.db.WithContext(ctx).Table((dom.Note{}).TableName()).Where("ticket_id=?", ticketID).Order("create_time DESC").Find(&out).Error
	return out, err
}
func (r *Repository) CreateNote(ctx context.Context, v *dom.Note) error {
	return r.db.WithContext(ctx).Table(v.TableName()).Create(v).Error
}
func (r *Repository) UpdateNote(ctx context.Context, ticketID, id int64, content string) error {
	result := r.db.WithContext(ctx).Table((dom.Note{}).TableName()).Where("ticket_id=? AND id=?", ticketID, id).Update("content", content)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
func (r *Repository) DeleteNote(ctx context.Context, ticketID, id int64) error {
	result := r.db.WithContext(ctx).Table((dom.Note{}).TableName()).Where("ticket_id=? AND id=?", ticketID, id).Delete(&dom.Note{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
func (r *Repository) ListLogs(ctx context.Context, ticketID int64) ([]dom.Log, error) {
	var out []dom.Log
	err := r.db.WithContext(ctx).Table((dom.Log{}).TableName()).Where("ticket_id=?", ticketID).Order("create_time DESC").Find(&out).Error
	return out, err
}
func (r *Repository) CreateForward(ctx context.Context, v *dom.Forward, fields map[string]any, log *dom.Log) error {
	return r.Transaction(ctx, func(tx *gorm.DB) error {
		if err := lockOpenTicket(tx, v.TicketID); err != nil {
			return err
		}
		if err := tx.Table(v.TableName()).Create(v).Error; err != nil {
			return err
		}
		fields["update_time"] = time.Now()
		if err := tx.Table((dom.Ticket{}).TableName()).Where("id=?", v.TicketID).Updates(fields).Error; err != nil {
			return err
		}
		return tx.Table(log.TableName()).Create(log).Error
	})
}

func (r *Repository) PollInterval(ctx context.Context) int {
	var raw string
	r.db.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`='ticket.poll_interval_seconds'").Scan(&raw)
	var result int
	if _, err := fmt.Sscanf(raw, "%d", &result); err != nil || result < 10 || result > 300 {
		return 30
	}
	return result
}

func (r *Repository) SaveType(ctx context.Context, v *dom.Type) error {
	if v.ID == 0 {
		return r.db.WithContext(ctx).Table(v.TableName()).Create(v).Error
	}
	return r.db.WithContext(ctx).Table(v.TableName()).Where("id=?", v.ID).Updates(map[string]any{"name": v.Name, "update_time": time.Now()}).Error
}
func (r *Repository) DeleteType(ctx context.Context, id int64) error {
	var count int64
	r.db.WithContext(ctx).Table((dom.Ticket{}).TableName()).Where("ticket_type_id=?", id).Count(&count)
	if count > 0 {
		return errors.New("该类型已有工单，不能删除")
	}
	return r.db.WithContext(ctx).Table((dom.Type{}).TableName()).Where("id=?", id).Delete(&dom.Type{}).Error
}
func (r *Repository) SaveStatus(ctx context.Context, v *dom.Status) error {
	return r.Transaction(ctx, func(tx *gorm.DB) error {
		if v.IsDefault {
			if err := tx.Table(v.TableName()).Where("id<>?", v.ID).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		if v.ID == 0 {
			return tx.Table(v.TableName()).Create(v).Error
		}
		return tx.Table(v.TableName()).Where("id=?", v.ID).Updates(map[string]any{"name": v.Name, "color": v.Color, "status": v.Final, "is_default": v.IsDefault, "update_time": time.Now()}).Error
	})
}
func (r *Repository) DeleteStatus(ctx context.Context, id int64) error {
	if id >= 1 && id <= 5 {
		return errors.New("系统状态不能删除")
	}
	var count int64
	r.db.WithContext(ctx).Table((dom.Ticket{}).TableName()).Where("status=?", id).Count(&count)
	if count > 0 {
		return errors.New("该状态正在使用，不能删除")
	}
	return r.db.WithContext(ctx).Table((dom.Status{}).TableName()).Where("id=?", id).Delete(&dom.Status{}).Error
}
func (r *Repository) ListPrereplies(ctx context.Context) ([]dom.Prereply, error) {
	var out []dom.Prereply
	err := r.db.WithContext(ctx).Table((dom.Prereply{}).TableName()).Order("sort ASC,id ASC").Find(&out).Error
	return out, err
}
func (r *Repository) SavePrereply(ctx context.Context, v *dom.Prereply) error {
	if v.ID == 0 {
		return r.db.WithContext(ctx).Table(v.TableName()).Create(v).Error
	}
	return r.db.WithContext(ctx).Table(v.TableName()).Where("id=?", v.ID).Updates(map[string]any{"content": v.Content, "sort": v.Sort, "update_time": time.Now()}).Error
}
func (r *Repository) DeletePrereply(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Table((dom.Prereply{}).TableName()).Where("id=?", id).Delete(&dom.Prereply{}).Error
}
func (r *Repository) OrderPrereplies(ctx context.Context, ids []int64) error {
	return r.Transaction(ctx, func(tx *gorm.DB) error {
		for i, id := range ids {
			if err := tx.Table((dom.Prereply{}).TableName()).Where("id=?", id).Update("sort", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *Repository) ListAdmins(ctx context.Context) ([]map[string]any, error) {
	var rows []struct {
		ID   int64
		Name string
	}
	err := r.db.WithContext(ctx).Table("system.admins").Select("id, real_name AS name").Where("status=1").Order("real_name ASC").Scan(&rows).Error
	out := make([]map[string]any, 0, len(rows))
	for _, v := range rows {
		out = append(out, map[string]any{"id": fmt.Sprint(v.ID), "name": v.Name})
	}
	return out, err
}
