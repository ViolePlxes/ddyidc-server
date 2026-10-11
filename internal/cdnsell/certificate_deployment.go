package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	cryptoutil "ddyidc/pkg/crypto"
	"errors"
	"gorm.io/gorm"
)

type certificateVerifier interface {
	VerifyCertificate(context.Context, provider.Account, string, string) error
}

func (s *Service) verifyCertificateDeployment(ctx context.Context, cert CertificateRecord, domain DomainRecord, account provider.Account) error {
	verifier, ok := s.cloud.(certificateVerifier)
	if !ok {
		return errors.New("证书实际生效核验配置后可用")
	}
	certificate, err := cryptoutil.Decrypt(cert.CertEncrypted, s.key)
	if err != nil {
		return errors.New("证书读取失败")
	}
	if err = verifier.VerifyCertificate(ctx, account, domain.ServiceNo, certificate); err != nil {
		return err
	}
	certificate = ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table(certsTable).Where("domain_id=? AND id<>? AND deploy_status='deployed'", domain.ID, cert.ID).Update("deploy_status", "undepolyed").Error; e != nil {
			return e
		}
		return tx.Table(certsTable).Where("id=? AND deploy_status='pending'", cert.ID).Update("deploy_status", "deployed").Error
	})
	if err != nil {
		return err
	}
	return s.updateCapability(ctx, "DeployCertificate", true, "节点TLS域名、信任链及证书指纹核验成功")
}
