package admintodo

import (
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"ddyidc/pkg/crypto"
	"encoding/hex"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type EvidenceFile struct {
	ID         string    `json:"id" gorm:"primaryKey"`
	CustomerID int64     `json:"-"`
	Mime       string    `json:"mime"`
	Size       int64     `json:"size"`
	Checksum   string    `json:"-"`
	Ciphertext string    `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (EvidenceFile) TableName() string { return "payment.todo_evidence_files" }
func validateEvidence(row EvidenceFile, key []byte) (string, error) {
	raw, err := crypto.Decrypt(row.Ciphertext, key)
	sum := sha256.Sum256([]byte(raw))
	if err != nil || row.Size <= 0 || row.Size > 6<<20 || int64(len(raw)) != row.Size || hex.EncodeToString(sum[:]) != row.Checksum {
		return "", errors.New("invalid receipt integrity")
	}
	mime := http.DetectContentType([]byte(raw))
	if mime != row.Mime || (mime != "image/png" && mime != "image/jpeg" && mime != "application/pdf") {
		return "", errors.New("invalid receipt type")
	}
	return raw, nil
}
func registerEvidence(server *rest.Server, db *gorm.DB, manager *auth.Manager, key []byte) {
	upload := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 7<<20)
		if r.ParseMultipartForm(7<<20) != nil {
			reply(w, 413, nil, "凭证最大6MiB")
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		file, _, e := r.FormFile("file")
		if e != nil {
			reply(w, 400, nil, "请选择凭证文件")
			return
		}
		defer file.Close()
		raw, e := io.ReadAll(io.LimitReader(file, (6<<20)+1))
		if e != nil || len(raw) == 0 || len(raw) > 6<<20 {
			reply(w, 413, nil, "凭证最大6MiB")
			return
		}
		mime := http.DetectContentType(raw)
		if mime != "image/png" && mime != "image/jpeg" && mime != "application/pdf" {
			reply(w, 400, nil, "仅接受PNG、JPEG和PDF凭证")
			return
		}
		cipher, e := crypto.Encrypt(string(raw), key)
		if e != nil {
			reply(w, 503, nil, "凭证保存暂不可用")
			return
		}
		sum := sha256.Sum256(raw)
		row := EvidenceFile{ID: strconv.FormatInt(snowflake.GenID(), 10), CustomerID: c.UserID, Mime: mime, Size: int64(len(raw)), Checksum: hex.EncodeToString(sum[:]), Ciphertext: cipher, CreatedAt: time.Now().UTC()}
		if db.WithContext(r.Context()).Create(&row).Error != nil {
			reply(w, 503, nil, "凭证保存失败")
			return
		}
		reply(w, 200, row, "")
	}
	download := func(admin bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			kind := auth.SessionUser
			if admin {
				kind = auth.SessionAdmin
			}
			c, e := manager.ParseAccessTokenFor(r.Context(), kind, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if e != nil || c == nil {
				reply(w, 401, nil, "请先登录")
				return
			}
			if admin && !IsSuper(r.Context(), db, c.UserID) {
				reply(w, 403, nil, "无权查看凭证")
				return
			}
			id := pathvar.Vars(r)["id"]
			var row EvidenceFile
			q := db.WithContext(r.Context()).Where("id=?", id)
			if !admin {
				q = q.Where("customer_id=?", c.UserID)
			}
			if q.Take(&row).Error != nil {
				reply(w, 404, nil, "凭证不存在")
				return
			}
			var bound int64
			if db.WithContext(r.Context()).Model(&OfflineTransfer{}).Where("evidence_id=? AND customer_id=?", id, row.CustomerID).Count(&bound).Error != nil || bound == 0 {
				reply(w, 404, nil, "凭证未关联申请")
				return
			}
			raw, e := validateEvidence(row, key)
			if e != nil {
				reply(w, 503, nil, "凭证损坏，请补充材料")
				return
			}
			if admin {
				var bound OfflineTransfer
				if db.WithContext(r.Context()).Where("evidence_id=? AND customer_id=?", id, row.CustomerID).Order("created_at DESC,id DESC").Take(&bound).Error != nil {
					reply(w, 404, nil, "凭证关联不存在")
					return
				}
				if db.WithContext(r.Context()).Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": strconv.FormatInt(snowflake.GenID(), 10), "kind": "offline-transfers", "source": "offline", "object_id": bound.ID, "action": "view_evidence", "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": "下载原始到账凭证", "created_at": time.Now().UTC()}).Error != nil {
					reply(w, 503, nil, "凭证访问审计保存失败")
					return
				}
			}
			extension := "pdf"
			if row.Mime == "image/png" {
				extension = "png"
			}
			if row.Mime == "image/jpeg" {
				extension = "jpg"
			}
			w.Header().Set("Content-Type", row.Mime)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Disposition", "attachment; filename=receipt."+extension)
			_, _ = w.Write([]byte(raw))
		}
	}
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/v1/payment/todo-evidence", Handler: upload})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/payment/todo-evidence/:id", Handler: download(false)})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/payment/todo-evidence/:id", Handler: download(true)})
}
