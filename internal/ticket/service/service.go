package ticket

import (
	"context"
	"crypto/rand"
	dom "ddyidc/internal/ticket/domain"
	repo "ddyidc/internal/ticket/repository"
	types "ddyidc/internal/ticket/types"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/gorm"
)

var (
	ErrNotFound         = errors.New("工单不存在")
	ErrForbidden        = errors.New("没有权限访问该工单")
	ErrClosed           = repo.ErrClosed
	attachmentIDPattern = regexp.MustCompile(`^[a-f0-9-]{36}$`)
)

type NotificationHooks interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
	NotifyAdmins(context.Context, string, []int64, string, map[string]string) error
}

type Service struct {
	repo     *repo.Repository
	notifier NotificationHooks
}

func NewService(repository *repo.Repository) *Service     { return &Service{repo: repository} }
func (s *Service) EnsureSchema(ctx context.Context) error { return s.repo.EnsureSchema(ctx) }
func (s *Service) SetNotifier(notifier NotificationHooks) { s.notifier = notifier }

func validateAttachments(items []dom.Attachment) error {
	if len(items) > 5 {
		return errors.New("每次最多上传 5 个附件")
	}
	for _, v := range items {
		if !attachmentIDPattern.MatchString(v.ID) || strings.TrimSpace(v.Name) == "" || v.Size < 0 || v.Size > 10<<20 {
			return errors.New("附件信息无效")
		}
	}
	return nil
}
func marshalAttachments(items []dom.Attachment) json.RawMessage {
	if len(items) == 0 {
		return json.RawMessage("[]")
	}
	clean := make([]dom.Attachment, len(items))
	copy(clean, items)
	for i := range clean {
		clean[i].URL = ""
	}
	raw, _ := json.Marshal(clean)
	return raw
}
func unmarshalAttachments(raw json.RawMessage) []dom.Attachment {
	var out []dom.Attachment
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = []dom.Attachment{}
	}
	return out
}
func decorateAttachments(items []dom.Attachment, ticketID int64, actorType string) []dom.Attachment {
	prefix := "/api/v1/tickets"
	if actorType == dom.ActorAdmin {
		prefix = "/admin/v1/tickets"
	}
	for i := range items {
		items[i].URL = fmt.Sprintf("%s/%d/attachments/%s", prefix, ticketID, items[i].ID)
	}
	return items
}

func (s *Service) newTicketNumber(ctx context.Context) (string, error) {
	for i := 0; i < 8; i++ {
		buf := make([]byte, 7)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for j := range buf {
			buf[j] = '0' + buf[j]%10
		}
		number := "YHGD" + string(buf)
		if _, err := s.repo.FindTicketByNumber(ctx, number); errors.Is(err, gorm.ErrRecordNotFound) {
			return number, nil
		}
	}
	return "", errors.New("生成工单号失败")
}

func (s *Service) Create(ctx context.Context, clientID int64, input types.CreateInput) (*types.DetailView, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	if input.Title == "" || len([]rune(input.Title)) > 128 {
		return nil, errors.New("标题长度应为 1-128 个字符")
	}
	if input.Content == "" || len([]rune(input.Content)) > 20000 {
		return nil, errors.New("工单正文长度应为 1-20000 个字符")
	}
	if err := validateAttachments(input.Attachments); err != nil {
		return nil, err
	}
	if _, err := s.repo.FindType(ctx, input.TicketTypeID); err != nil {
		return nil, errors.New("工单类型不存在")
	}
	if input.HostID != nil {
		host, err := s.repo.CheckHost(ctx, clientID, *input.HostID)
		if err != nil {
			return nil, errors.New("关联产品不存在或不属于当前客户")
		}
		if host.Status == 6 || host.DeletedAt != nil {
			return nil, errors.New("已删除产品不能关联工单")
		}
	}
	number, err := s.newTicketNumber(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ticket := dom.Ticket{TicketNum: number, ClientID: clientID, Title: input.Title, TicketTypeID: input.TicketTypeID, Content: input.Content, StatusID: dom.StatusPending, AttachmentJSON: marshalAttachments(input.Attachments), LastReplyTime: now, PostTime: now, CreatedAt: now, UpdatedAt: now}
	reply := dom.Reply{Type: dom.ActorClient, RelID: clientID, Content: input.Content, AttachmentJSON: ticket.AttachmentJSON, CreatedAt: now}
	log := dom.Log{ActorType: dom.ActorClient, ActorID: clientID, Action: "create", Detail: "客户创建工单", CreatedAt: now}
	if err = s.repo.Transaction(ctx, func(tx *gorm.DB) error { return s.repo.CreateTicket(tx, &ticket, &reply, input.HostID, &log) }); err != nil {
		return nil, err
	}
	_ = s.repo.MarkRead(ctx, ticket.ID, dom.ActorClient, clientID, reply.ID)
	s.notifyAdmins(ctx, &ticket, "created", "有新工单待处理", fmt.Sprintf("%s：%s", ticket.TicketNum, ticket.Title))
	return s.Detail(ctx, dom.ActorClient, clientID, ticket.ID)
}

func (s *Service) notifyAdmins(ctx context.Context, ticket *dom.Ticket, kind, title, content string) {
	ids, err := s.repo.ActiveAdminIDs(ctx, ticket.TicketTypeID, ticket.AdminID)
	if err != nil {
		return
	}
	items := make([]dom.Notification, 0, len(ids))
	for _, id := range ids {
		items = append(items, dom.Notification{TicketID: ticket.ID, RecipientType: dom.ActorAdmin, RecipientID: id, Kind: kind, Title: title, Content: content, CreatedAt: time.Now()})
	}
	_ = s.repo.CreateNotifications(ctx, items)
	if s.notifier != nil {
		scene := "ticket_reply"
		if kind == "created" {
			scene = "ticket_new"
		}
		params := map[string]string{"ticket_no": ticket.TicketNum, "title": ticket.Title}
		businessID := fmt.Sprintf("%d:%s:%d", ticket.ID, kind, ticket.LastReplyTime.UnixNano())
		if notifyErr := s.notifier.NotifyAdmins(ctx, scene, ids, businessID, params); notifyErr != nil {
			logx.Errorf("ticket admin notification failed ticket_id=%d: %v", ticket.ID, notifyErr)
		}
	}
}
func (s *Service) notifyClient(ctx context.Context, ticket *dom.Ticket, kind, title, content string) {
	_ = s.repo.CreateNotifications(ctx, []dom.Notification{{TicketID: ticket.ID, RecipientType: dom.ActorClient, RecipientID: ticket.ClientID, Kind: kind, Title: title, Content: content, CreatedAt: time.Now()}})
	if s.notifier != nil {
		params := map[string]string{"ticket_no": ticket.TicketNum, "title": ticket.Title}
		businessID := fmt.Sprintf("%d:%s:%d", ticket.ID, kind, ticket.LastReplyTime.UnixNano())
		if notifyErr := s.notifier.NotifyCustomer(ctx, "ticket_reply", ticket.ClientID, businessID, params); notifyErr != nil {
			logx.Errorf("ticket client notification failed ticket_id=%d: %v", ticket.ID, notifyErr)
		}
	}
}

func (s *Service) authorize(ctx context.Context, actorType string, actorID, ticketID int64) (*dom.Ticket, error) {
	v, err := s.repo.FindTicket(ctx, ticketID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if actorType == dom.ActorClient && v.ClientID != actorID {
		return nil, ErrForbidden
	}
	if actorType != dom.ActorClient && actorType != dom.ActorAdmin {
		return nil, ErrForbidden
	}
	return v, nil
}

func (s *Service) ticketView(ctx context.Context, v *dom.Ticket, actorType string, actorID int64) (types.TicketView, error) {
	status, err := s.repo.FindStatus(ctx, v.StatusID)
	if err != nil {
		return types.TicketView{}, err
	}
	meta, _ := s.repo.TicketMeta(ctx, v)
	return types.TicketView{ID: v.ID, TicketNum: v.TicketNum, ClientID: v.ClientID, ClientName: meta.ClientName, Title: v.Title, TicketTypeID: v.TicketTypeID, TypeName: meta.TypeName, Status: *status, Content: v.Content, Attachments: decorateAttachments(unmarshalAttachments(v.AttachmentJSON), v.ID, actorType), HostID: meta.HostID, HostName: meta.HostName, AdminID: v.AdminID, AdminName: meta.AdminName, UnreadCount: s.repo.UnreadCount(ctx, v.ID, actorType, actorID), LastReplyID: s.repo.LastReplyID(ctx, v.ID), LastReplyTime: v.LastReplyTime, PostTime: v.PostTime, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}, nil
}

func (s *Service) replyViews(ctx context.Context, replies []dom.Reply, actorType string) ([]types.ReplyView, error) {
	out := make([]types.ReplyView, 0, len(replies))
	for _, v := range replies {
		item := types.ReplyView{ID: v.ID, Type: v.Type, RelID: v.RelID, Content: v.Content, Attachments: decorateAttachments(unmarshalAttachments(v.AttachmentJSON), v.TicketID, actorType), QuoteReplyID: v.QuoteReplyID, CreatedAt: v.CreatedAt}
		if v.QuoteReplyID != nil {
			quote, err := s.repo.FindReply(ctx, v.TicketID, *v.QuoteReplyID)
			if err == nil {
				item.Quote = &types.QuoteView{ID: quote.ID, Type: quote.Type, Content: quote.Content}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) List(ctx context.Context, actorType string, actorID int64, page, pageSize int, statusID, typeID *int64, keyword, since string) (types.ListResult, error) {
	var clientID *int64
	if actorType == dom.ActorClient {
		clientID = &actorID
	}
	rows, total, err := s.repo.ListTickets(ctx, clientID, page, pageSize, statusID, typeID, keyword, since)
	if err != nil {
		return types.ListResult{}, err
	}
	list := make([]types.TicketView, 0, len(rows))
	for i := range rows {
		v, viewErr := s.ticketView(ctx, &rows[i], actorType, actorID)
		if viewErr != nil {
			return types.ListResult{}, viewErr
		}
		list = append(list, v)
	}
	unread, _ := s.Unread(ctx, actorType, actorID)
	return types.ListResult{List: list, Total: total, Page: page, PageSize: pageSize, UnreadCount: unread, ServerTime: time.Now(), PollIntervalSeconds: s.repo.PollInterval(ctx)}, nil
}
func (s *Service) Unread(ctx context.Context, actorType string, actorID int64) (int64, error) {
	var clientID *int64
	if actorType == dom.ActorClient {
		clientID = &actorID
	}
	rows, _, err := s.repo.ListTickets(ctx, clientID, 1, 10000, nil, nil, "", "")
	if err != nil {
		return 0, err
	}
	var total int64
	for i := range rows {
		total += s.repo.UnreadCount(ctx, rows[i].ID, actorType, actorID)
	}
	return total, nil
}
func (s *Service) Statistic(ctx context.Context, clientID int64) (map[string]int64, error) {
	out := map[string]int64{}
	statuses, err := s.repo.ListStatuses(ctx)
	if err != nil {
		return nil, err
	}
	for _, status := range statuses {
		sid := status.ID
		_, count, e := s.repo.ListTickets(ctx, &clientID, 1, 1, &sid, nil, "", "")
		if e != nil {
			return nil, e
		}
		out[fmt.Sprint(status.ID)] = count
	}
	return out, nil
}

func (s *Service) Detail(ctx context.Context, actorType string, actorID, ticketID int64) (*types.DetailView, error) {
	ticket, err := s.authorize(ctx, actorType, actorID, ticketID)
	if err != nil {
		return nil, err
	}
	view, err := s.ticketView(ctx, ticket, actorType, actorID)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListReplies(ctx, ticketID, 0)
	if err != nil {
		return nil, err
	}
	replies, _ := s.replyViews(ctx, rows, actorType)
	last := view.LastReplyID
	_ = s.repo.MarkRead(ctx, ticketID, actorType, actorID, last)
	s.repo.MarkTicketNotificationsRead(ctx, ticketID, actorType, actorID)
	view.UnreadCount = 0
	result := &types.DetailView{Ticket: view, Replies: replies, CanReply: ticket.StatusID != dom.StatusClosed, CanUrge: actorType == dom.ActorClient && ticket.StatusID != dom.StatusClosed && time.Since(ticket.PostTime) >= 30*time.Minute, CanClose: ticket.StatusID != dom.StatusClosed, PollIntervalSeconds: s.repo.PollInterval(ctx)}
	if actorType == dom.ActorAdmin {
		result.Notes, _ = s.repo.ListNotes(ctx, ticketID)
		result.Logs, _ = s.repo.ListLogs(ctx, ticketID)
	}
	return result, nil
}
func (s *Service) Replies(ctx context.Context, actorType string, actorID, ticketID, since int64) ([]types.ReplyView, error) {
	if _, err := s.authorize(ctx, actorType, actorID, ticketID); err != nil {
		return nil, err
	}
	rows, err := s.repo.ListReplies(ctx, ticketID, since)
	if err != nil {
		return nil, err
	}
	views, _ := s.replyViews(ctx, rows, actorType)
	if len(rows) > 0 {
		_ = s.repo.MarkRead(ctx, ticketID, actorType, actorID, rows[len(rows)-1].ID)
		s.repo.MarkTicketNotificationsRead(ctx, ticketID, actorType, actorID)
	}
	return views, nil
}

func (s *Service) Reply(ctx context.Context, actorType string, actorID, ticketID int64, input types.ReplyInput) (*types.ReplyView, error) {
	ticket, err := s.authorize(ctx, actorType, actorID, ticketID)
	if err != nil {
		return nil, err
	}
	if ticket.StatusID == dom.StatusClosed {
		return nil, ErrClosed
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len([]rune(input.Content)) > 20000 {
		return nil, errors.New("回复内容长度应为 1-20000 个字符")
	}
	if err := validateAttachments(input.Attachments); err != nil {
		return nil, err
	}
	if input.QuoteReplyID != nil {
		if _, err := s.repo.FindReply(ctx, ticketID, *input.QuoteReplyID); err != nil {
			return nil, errors.New("引用回复不存在")
		}
	}
	if actorType == dom.ActorClient {
		if host, hostErr := s.repo.LinkedHost(ctx, ticketID); hostErr == nil && (host.DeletedAt != nil || host.Status == 5 || host.Status == 6 || (host.ExpiresAt != nil && host.ExpiresAt.Before(time.Now()))) {
			return nil, errors.New("关联产品已到期或已删除，不能回复")
		}
	}
	now := time.Now()
	reply := dom.Reply{TicketID: ticketID, Type: actorType, RelID: actorID, Content: input.Content, AttachmentJSON: marshalAttachments(input.Attachments), QuoteReplyID: input.QuoteReplyID, CreatedAt: now}
	status := dom.StatusUserReply
	fields := map[string]any{"status": status, "last_reply_time": now, "post_time": now}
	detail := "客户回复工单"
	if actorType == dom.ActorAdmin {
		status = dom.StatusReplied
		fields["status"] = status
		fields["last_reply_admin_id"] = actorID
		if ticket.AdminID == nil {
			fields["admin_id"] = actorID
			ticket.AdminID = &actorID
		}
		detail = "管理员回复工单"
	}
	log := dom.Log{TicketID: ticketID, ActorType: actorType, ActorID: actorID, Action: "reply", Detail: detail, CreatedAt: now}
	var note *dom.Note
	if actorType == dom.ActorAdmin && strings.TrimSpace(input.Note) != "" {
		note = &dom.Note{TicketID: ticketID, Content: strings.TrimSpace(input.Note), AdminID: actorID, CreatedAt: now}
	}
	if err := s.repo.CreateReply(ctx, &reply, fields, &log, note); err != nil {
		return nil, err
	}
	_ = s.repo.MarkRead(ctx, ticketID, actorType, actorID, reply.ID)
	ticket.StatusID = status
	ticket.LastReplyTime = now
	if actorType == dom.ActorAdmin {
		s.notifyClient(ctx, ticket, "reply", "工单收到新回复", fmt.Sprintf("%s：%s", ticket.TicketNum, ticket.Title))
	} else {
		s.notifyAdmins(ctx, ticket, "reply", "客户回复了工单", fmt.Sprintf("%s：%s", ticket.TicketNum, ticket.Title))
	}
	views, _ := s.replyViews(ctx, []dom.Reply{reply}, actorType)
	return &views[0], nil
}

func (s *Service) Urge(ctx context.Context, clientID, ticketID int64) error {
	ticket, err := s.authorize(ctx, dom.ActorClient, clientID, ticketID)
	if err != nil {
		return err
	}
	if ticket.StatusID == dom.StatusClosed {
		return ErrClosed
	}
	if time.Since(ticket.PostTime) < 30*time.Minute {
		return errors.New("每 30 分钟只能催单一次")
	}
	now := time.Now()
	log := dom.Log{TicketID: ticketID, ActorType: dom.ActorClient, ActorID: clientID, Action: "urge", Detail: "客户催单", CreatedAt: now}
	if err := s.repo.UpdateTicket(ctx, ticketID, map[string]any{"post_time": now}, &log); err != nil {
		return err
	}
	ticket.PostTime = now
	s.notifyAdmins(ctx, ticket, "urge", "客户催单提醒", fmt.Sprintf("%s：%s", ticket.TicketNum, ticket.Title))
	return nil
}
func (s *Service) Close(ctx context.Context, actorType string, actorID, ticketID int64) error {
	ticket, err := s.authorize(ctx, actorType, actorID, ticketID)
	if err != nil {
		return err
	}
	if ticket.StatusID == dom.StatusClosed {
		return nil
	}
	log := dom.Log{TicketID: ticketID, ActorType: actorType, ActorID: actorID, Action: "close", Detail: "关闭工单", CreatedAt: time.Now()}
	return s.repo.UpdateTicket(ctx, ticketID, map[string]any{"status": dom.StatusClosed}, &log)
}
func (s *Service) Receive(ctx context.Context, adminID, ticketID int64) error {
	ticket, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID)
	if err != nil {
		return err
	}
	if ticket.StatusID == dom.StatusClosed {
		return ErrClosed
	}
	log := dom.Log{TicketID: ticketID, ActorType: dom.ActorAdmin, ActorID: adminID, Action: "receive", Detail: "管理员接单", CreatedAt: time.Now()}
	return s.repo.UpdateTicket(ctx, ticketID, map[string]any{"status": dom.StatusProcessing, "admin_id": adminID}, &log)
}
func (s *Service) Resolve(ctx context.Context, adminID, ticketID int64) error {
	ticket, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID)
	if err != nil {
		return err
	}
	if ticket.StatusID == dom.StatusClosed {
		return ErrClosed
	}
	log := dom.Log{TicketID: ticketID, ActorType: dom.ActorAdmin, ActorID: adminID, Action: "resolved", Detail: "标记为已解决", CreatedAt: time.Now()}
	return s.repo.UpdateTicket(ctx, ticketID, map[string]any{"status": dom.StatusReplied}, &log)
}
func (s *Service) Forward(ctx context.Context, adminID, ticketID int64, input types.ForwardInput) error {
	ticket, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID)
	if err != nil {
		return err
	}
	if input.ForwardAdminID == nil && input.TicketTypeID == nil {
		return errors.New("请选择目标管理员或工单类型")
	}
	fields := map[string]any{}
	if input.ForwardAdminID != nil {
		fields["admin_id"] = *input.ForwardAdminID
	}
	if input.TicketTypeID != nil {
		if _, e := s.repo.FindType(ctx, *input.TicketTypeID); e != nil {
			return errors.New("目标工单类型不存在")
		}
		fields["ticket_type_id"] = *input.TicketTypeID
		ticket.TicketTypeID = *input.TicketTypeID
	}
	forward := dom.Forward{TicketID: ticketID, AdminID: adminID, ForwardAdminID: input.ForwardAdminID, TicketTypeID: input.TicketTypeID, Notes: strings.TrimSpace(input.Notes), CreatedAt: time.Now()}
	log := dom.Log{TicketID: ticketID, ActorType: dom.ActorAdmin, ActorID: adminID, Action: "forward", Detail: "转交工单：" + forward.Notes, CreatedAt: time.Now()}
	if err := s.repo.CreateForward(ctx, &forward, fields, &log); err != nil {
		return err
	}
	ticket.AdminID = input.ForwardAdminID
	s.notifyAdmins(ctx, ticket, "forward", "收到转交工单", fmt.Sprintf("%s：%s", ticket.TicketNum, ticket.Title))
	return nil
}

func (s *Service) Products(ctx context.Context, clientID int64) ([]types.ProductView, error) {
	rows, err := s.repo.ListClientProducts(ctx, clientID)
	if err != nil {
		return nil, err
	}
	out := make([]types.ProductView, 0, len(rows))
	now := time.Now()
	for _, v := range rows {
		can := v.DeletedAt == nil && v.Status != 5 && v.Status != 6 && (v.ExpiresAt == nil || v.ExpiresAt.After(now))
		out = append(out, types.ProductView{ID: v.ID, Name: v.Name, Status: v.Status, ExpiresAt: v.ExpiresAt, CanReply: can})
	}
	return out, nil
}
func (s *Service) Types(ctx context.Context) ([]dom.Type, error) { return s.repo.ListTypes(ctx) }
func (s *Service) Statuses(ctx context.Context) ([]dom.Status, error) {
	return s.repo.ListStatuses(ctx)
}
func (s *Service) Prereplies(ctx context.Context) ([]dom.Prereply, error) {
	return s.repo.ListPrereplies(ctx)
}
func (s *Service) Admins(ctx context.Context) ([]map[string]any, error) {
	return s.repo.ListAdmins(ctx)
}
func (s *Service) Notifications(ctx context.Context, actorType string, actorID int64) (map[string]any, error) {
	items, unread, err := s.repo.ListNotifications(ctx, actorType, actorID, 50)
	return map[string]any{"list": items, "unreadCount": unread}, err
}

func (s *Service) SaveType(ctx context.Context, id int64, input types.ConfigInput) (*dom.Type, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 64 {
		return nil, errors.New("类型名称不能为空且最多 64 个字符")
	}
	v := &dom.Type{ID: id, Name: name}
	if err := s.repo.SaveType(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}
func (s *Service) DeleteType(ctx context.Context, id int64) error { return s.repo.DeleteType(ctx, id) }
func (s *Service) SaveStatus(ctx context.Context, id int64, input types.ConfigInput) (*dom.Status, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 32 {
		return nil, errors.New("状态名称不能为空且最多 32 个字符")
	}
	color := strings.TrimSpace(input.Color)
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(color) {
		return nil, errors.New("状态颜色必须为 #RRGGBB")
	}
	v := &dom.Status{ID: id, Name: name, Color: color, Final: input.Final, IsDefault: input.IsDefault}
	if err := s.repo.SaveStatus(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}
func (s *Service) DeleteStatus(ctx context.Context, id int64) error {
	return s.repo.DeleteStatus(ctx, id)
}
func (s *Service) SavePrereply(ctx context.Context, id int64, input types.ConfigInput) (*dom.Prereply, error) {
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > 5000 {
		return nil, errors.New("预设回复不能为空且最多 5000 个字符")
	}
	v := &dom.Prereply{ID: id, Content: content, Sort: input.Sort}
	if err := s.repo.SavePrereply(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}
func (s *Service) DeletePrereply(ctx context.Context, id int64) error {
	return s.repo.DeletePrereply(ctx, id)
}
func (s *Service) OrderPrereplies(ctx context.Context, ids []int64) error {
	return s.repo.OrderPrereplies(ctx, ids)
}
func (s *Service) AddNote(ctx context.Context, adminID, ticketID int64, content string) (*dom.Note, error) {
	if _, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID); err != nil {
		return nil, err
	}
	content = strings.TrimSpace(content)
	if content == "" || len([]rune(content)) > 5000 {
		return nil, errors.New("备注不能为空且最多 5000 个字符")
	}
	v := &dom.Note{TicketID: ticketID, Content: content, AdminID: adminID, CreatedAt: time.Now()}
	if err := s.repo.CreateNote(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}
func (s *Service) DeleteNote(ctx context.Context, adminID, ticketID, noteID int64) error {
	if _, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID); err != nil {
		return err
	}
	return s.repo.DeleteNote(ctx, ticketID, noteID)
}
func (s *Service) UpdateNote(ctx context.Context, adminID, ticketID, noteID int64, content string) error {
	if _, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID); err != nil {
		return err
	}
	content = strings.TrimSpace(content)
	if content == "" || len([]rune(content)) > 5000 {
		return errors.New("备注不能为空且最多 5000 个字符")
	}
	return s.repo.UpdateNote(ctx, ticketID, noteID, content)
}
func (s *Service) Logs(ctx context.Context, adminID, ticketID int64) ([]dom.Log, error) {
	if _, err := s.authorize(ctx, dom.ActorAdmin, adminID, ticketID); err != nil {
		return nil, err
	}
	return s.repo.ListLogs(ctx, ticketID)
}

func (s *Service) Attachment(ctx context.Context, actorType string, actorID, ticketID int64, fileID string) (dom.Attachment, error) {
	ticket, err := s.authorize(ctx, actorType, actorID, ticketID)
	if err != nil {
		return dom.Attachment{}, err
	}
	for _, v := range unmarshalAttachments(ticket.AttachmentJSON) {
		if v.ID == fileID {
			return v, nil
		}
	}
	replies, err := s.repo.ListReplies(ctx, ticketID, 0)
	if err != nil {
		return dom.Attachment{}, err
	}
	for _, reply := range replies {
		for _, v := range unmarshalAttachments(reply.AttachmentJSON) {
			if v.ID == fileID {
				return v, nil
			}
		}
	}
	return dom.Attachment{}, errors.New("附件不存在")
}
