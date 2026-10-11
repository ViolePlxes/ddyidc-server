package cdnsell

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type certificateProofCloud struct {
	provider.Provider
	failure error
}

func (f *certificateProofCloud) VerifyCertificate(context.Context, provider.Account, string, string) error {
	return f.failure
}
func testCertificate(t *testing.T, domain string, start, end time.Time) (string, string) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{domain}, NotBefore: start, NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
}
func TestCertificateProofAndQueueMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	service := NewService(db, []byte("01234567890123456789012345678901"), nil)
	ctx := context.Background()
	id := snowflake.GenID()
	user := int64(778811)
	name := fmt.Sprintf("cert-%d.example.com", id)
	if e := db.Exec("INSERT INTO system.cdnsell_domains(id,user_id,domain,business_type,origin_type,origin_host,origin_protocol,upstream_status,service_no) VALUES(?,?,?,'web','ip','1.1.1.1','http','active','isolated')", id, user, name).Error; e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	valid, key := testCertificate(t, name, now.Add(-time.Hour), now.Add(time.Hour))
	expired, expiredKey := testCertificate(t, name, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if _, e := service.UploadCertificate(ctx, user, id, "expired", expired, expiredKey); e == nil {
		t.Fatal("expired certificate stored")
	}
	future, futureKey := testCertificate(t, name, now.Add(time.Hour), now.Add(2*time.Hour))
	if _, e := service.UploadCertificate(ctx, user, id, "future", future, futureKey); e == nil {
		t.Fatal("future certificate stored")
	}
	if _, e := service.UploadCertificate(ctx, user+1, id, "wrong customer", valid, key); e == nil {
		t.Fatal("cross customer certificate stored")
	}
	cert, e := service.UploadCertificate(ctx, user, id, "valid", valid, key)
	if e != nil {
		t.Fatal(e)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := service.DeployCertificate(ctx, user, cert.ID); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("duplicate certificate queued", successes.Load())
	}
	var tasks int64
	db.Table(tasksTable).Where("biz_id=? AND task_type='cert_deploy'", cert.ID).Count(&tasks)
	if tasks != 1 {
		t.Fatal("duplicate task", tasks)
	}
	var domain DomainRecord
	db.Table(domainsTable).Where("id=?", id).Take(&domain)
	service.cloud = &certificateProofCloud{failure: errors.New("edge not ready")}
	if e := service.verifyCertificateDeployment(ctx, *cert, domain, provider.Account{}); e == nil {
		t.Fatal("failed proof accepted")
	}
	var saved CertificateRecord
	db.Table(certsTable).Where("id=?", cert.ID).Take(&saved)
	if saved.DeployStatus != "pending" {
		t.Fatal("unverified certificate marked deployed", saved.DeployStatus)
	}
	service.cloud = &certificateProofCloud{}
	if e := service.verifyCertificateDeployment(ctx, *cert, domain, provider.Account{}); e != nil {
		t.Fatal(e)
	}
	db.Table(certsTable).Where("id=?", cert.ID).Take(&saved)
	if saved.DeployStatus != "deployed" {
		t.Fatal("verified certificate not finalized")
	}
	if _, e := service.DeployCertificate(ctx, user, cert.ID); e == nil {
		t.Fatal("deployed certificate replay allowed")
	}
	t.Log("expired/future/cross-customer certificates denied; ten concurrent submissions queue once; failed edge proof remains pending")
}
