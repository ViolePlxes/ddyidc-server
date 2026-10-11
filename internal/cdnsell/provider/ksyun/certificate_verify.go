package ksyun

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"ddyidc/internal/cdnsell/provider"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"time"
)

// Verify the actual edge certificate, never an origin DNS address or a disabled
// TLS check. DNS is resolved once and the verified public address is pinned.
func (c *Client) VerifyCertificate(ctx context.Context, a provider.Account, id, certificate string) error {
	info, err := c.GetDomain(ctx, a, id)
	if err != nil {
		return err
	}
	domain, _ := info["domain"].(string)
	cname, _ := info["cname"].(string)
	cname = strings.TrimSuffix(strings.ToLower(cname), ".")
	if domain == "" || !(strings.HasSuffix(cname, ".ks-cdn.com") || strings.HasSuffix(cname, ".ksyuncdn.com")) {
		return errors.New("CDN证书核验地址无效")
	}
	block, _ := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("证书格式无效")
	}
	expected, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return errors.New("证书格式无效")
	}
	if err = expected.VerifyHostname(domain); err != nil {
		return errors.New("证书域名不匹配")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(certificate)) {
		return errors.New("证书格式无效")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, cname)
	if err != nil {
		return errors.New("CDN节点解析暂不可用")
	}
	var failures []error
	for _, address := range addresses {
		ip := address.IP
		if !publicEdgeIP(ip) {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := verifyEdgeTLS(probeCtx, net.JoinHostPort(ip.String(), "443"), domain, pool, expected)
		cancel()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		return nil
	}
	if len(failures) > 0 {
		return failures[0]
	}
	return errors.New("CDN节点没有可验证的公网地址")
}

// Exclude non-routable documentation, benchmarking and shared-address ranges,
// even where net.IP classifies them as global unicast.
func publicEdgeIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func verifyEdgeTLS(ctx context.Context, address, domain string, pool *x509.CertPool, expected *x509.Certificate) error {
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{ServerName: domain, RootCAs: pool, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("CDN节点TLS尚未验证通过")
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 || sha256.Sum256(state.PeerCertificates[0].Raw) != sha256.Sum256(expected.Raw) {
		return errors.New("CDN节点证书与本次上传不一致")
	}
	return nil
}
