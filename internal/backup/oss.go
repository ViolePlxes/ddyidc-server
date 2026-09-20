package backup

import (
	"context"
	"ddyidc/internal/shared/runtimepaths"
	"errors"
	"fmt"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func ossFailure(err error) error {
	var service oss.ServiceError
	if errors.As(err, &service) {
		code := service.Code
		if !regexp.MustCompile(`^[A-Za-z0-9_]{1,80}$`).MatchString(code) {
			code = "RequestFailed"
		}
		return fmt.Errorf("OSS 请求失败（%s），请检查权限、地域和网络配置", code)
	}
	return errors.New("OSS 连接失败或超时，请检查网络和配置")
}
func openBucket(ctx context.Context, c Config) (*oss.Bucket, error) {
	if e := validate(&c); e != nil {
		return nil, e
	}
	if c.AccessKeyID == "" || c.AccessKeySecret == "" || c.Bucket == "" {
		return nil, errors.New("OSS 尚未配置，配置后可用")
	}
	client, e := oss.New(c.Endpoint, c.AccessKeyID, c.AccessKeySecret, oss.Timeout(10, 60))
	if e != nil {
		return nil, errors.New("OSS 配置无效")
	}
	acl, e := client.GetBucketACL(c.Bucket, oss.WithContext(ctx))
	if e != nil {
		return nil, ossFailure(e)
	}
	if acl.ACL != string(oss.ACLPrivate) {
		return nil, errors.New("备份仅允许使用私有 Bucket，请先在阿里云控制台调整")
	}
	b, e := client.Bucket(c.Bucket)
	if e != nil {
		return nil, errors.New("Bucket 配置无效")
	}
	return b, nil
}
func (s *Service) Test(ctx context.Context, actor int64, ip string) error {
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	b, e := openBucket(ctx, c)
	if e != nil {
		return e
	}
	if _, e = b.ListObjects(oss.Prefix(c.Prefix+"backups/"), oss.MaxKeys(1), oss.WithContext(ctx)); e != nil {
		return ossFailure(e)
	}
	key := c.Prefix + "probes/" + uuid.NewString() + ".txt"
	if e = b.PutObject(key, strings.NewReader("DDYIDC backup permission probe"), oss.ObjectACL(oss.ACLPrivate), oss.WithContext(ctx)); e != nil {
		return ossFailure(e)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e = b.DeleteObject(key, oss.WithContext(cleanup)); e != nil {
		return errors.New("探针写入成功但删除失败，请授予该前缀 DeleteObject 权限")
	}
	return s.db.Create(&Audit{ActorID: actor, Action: "test_success", IP: ip}).Error
}
func (s *Service) upload(ctx context.Context, c Config, j *Job) error {
	b, e := openBucket(ctx, c)
	if e != nil {
		return e
	}
	key := c.Prefix + "backups/" + j.ID + ".tar.gz"
	if e = b.PutObjectFromFile(key, j.LocalPath, oss.ObjectACL(oss.ACLPrivate), oss.WithContext(ctx)); e != nil {
		return ossFailure(e)
	}
	headers, e := b.GetObjectDetailedMeta(key, oss.WithContext(ctx))
	if e != nil {
		return ossFailure(e)
	}
	size, e := strconv.ParseInt(headers.Get("Content-Length"), 10, 64)
	if e != nil || size != j.LocalBytes {
		return errors.New("OSS 上传大小校验失败")
	}
	j.ObjectKey = key
	j.OSSBytes = size
	j.Uploaded = true
	j.Endpoint = c.Endpoint
	j.Bucket = c.Bucket
	return nil
}
func (s *Service) Archive(ctx context.Context, j Job) (string, error) {
	if j.Kind == "restore" || j.SHA256 == "" {
		return "", errors.New("该记录没有可下载的业务备份")
	}
	if j.LocalPath != "" {
		if e := (runtimepaths.Paths{Root: s.Root}).Within(j.LocalPath); e != nil {
			return "", errors.New("备份路径无效")
		}
		if _, e := os.Stat(j.LocalPath); e == nil {
			sum, e := fileSHA(j.LocalPath)
			if e == nil && sum == j.SHA256 {
				return j.LocalPath, nil
			}
			return "", errors.New("本地备份完整性校验失败")
		}
	}
	if !j.Uploaded || j.Expired {
		return "", errors.New("本地备份已过期且没有可用 OSS 副本")
	}
	c, e := s.Config(ctx)
	if e != nil {
		return "", e
	}
	if c.Bucket != j.Bucket || c.Endpoint != j.Endpoint {
		return "", errors.New("请切换至该备份原始 Bucket 和 Endpoint 后重试")
	}
	b, e := openBucket(ctx, c)
	if e != nil {
		return "", e
	}
	f, e := os.CreateTemp(filepath.Join(s.Root, "backups/downloads"), j.ID+"-*.tar.gz")
	if e != nil {
		return "", errors.New("无法创建下载文件")
	}
	path := f.Name()
	f.Close()
	if e = b.GetObjectToFile(j.ObjectKey, path, oss.WithContext(ctx)); e != nil {
		os.Remove(path)
		return "", ossFailure(e)
	}
	sum, e := fileSHA(path)
	if e != nil || sum != j.SHA256 {
		os.Remove(path)
		return "", errors.New("OSS 备份完整性校验失败")
	}
	return path, nil
}
func (s *Service) pruneOSS(ctx context.Context, c Config) error {
	b, e := openBucket(ctx, c)
	if e != nil {
		return e
	}
	var jobs []Job
	if e = s.db.Where("uploaded=true AND expired=false AND bucket=? AND endpoint=? AND created_at<?", c.Bucket, c.Endpoint, time.Now().AddDate(0, 0, -c.RetentionDays)).Find(&jobs).Error; e != nil {
		return errors.New("读取备份保留记录失败")
	}
	for _, j := range jobs {
		// Only exact object keys previously uploaded and recorded by this system.
		if !strings.HasSuffix(j.ObjectKey, "/backups/"+j.ID+".tar.gz") {
			continue
		}
		if e = b.DeleteObject(j.ObjectKey, oss.WithContext(ctx)); e != nil {
			return ossFailure(e)
		}
		if e = s.db.Model(&Job{}).Where("id=?", j.ID).Update("expired", true).Error; e != nil {
			return errors.New("更新保留清理记录失败")
		}
	}
	return nil
}
