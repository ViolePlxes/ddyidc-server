package remoteconsole

import (
	"crypto/des"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"time"
)

// Authenticate to the supplier without disclosing its VNC secret to the browser.
// After authentication, advertise no-auth inside the already authenticated local session.
func authenticateRFB(upstream, client net.Conn, password string) error {
	_ = upstream.SetDeadline(time.Now().Add(20 * time.Second))
	_ = client.SetDeadline(time.Now().Add(20 * time.Second))
	defer upstream.SetDeadline(time.Time{})
	defer client.SetDeadline(time.Time{})
	banner := make([]byte, 12)
	if _, e := io.ReadFull(upstream, banner); e != nil {
		return e
	}
	if string(banner) != "RFB 003.008\n" && string(banner) != "RFB 003.007\n" {
		return fmt.Errorf("unsupported RFB version")
	}
	if _, e := upstream.Write([]byte("RFB 003.008\n")); e != nil {
		return e
	}
	var n [1]byte
	if _, e := io.ReadFull(upstream, n[:]); e != nil || n[0] == 0 {
		return fmt.Errorf("RFB negotiation failed")
	}
	types := make([]byte, int(n[0]))
	if _, e := io.ReadFull(upstream, types); e != nil {
		return e
	}
	chosen := byte(0)
	for _, v := range types {
		if v == 1 && chosen == 0 {
			chosen = 1
		}
		if v == 2 && password != "" {
			chosen = 2
		}
	}
	if chosen == 0 {
		return fmt.Errorf("unsupported RFB security")
	}
	if _, e := upstream.Write([]byte{chosen}); e != nil {
		return e
	}
	if chosen == 2 {
		challenge := make([]byte, 16)
		if _, e := io.ReadFull(upstream, challenge); e != nil {
			return e
		}
		key := make([]byte, 8)
		copy(key, []byte(password))
		for i := range key {
			key[i] = bits.Reverse8(key[i])
		}
		cipher, e := des.NewCipher(key)
		if e != nil {
			return e
		}
		cipher.Encrypt(challenge[:8], challenge[:8])
		cipher.Encrypt(challenge[8:], challenge[8:])
		if _, e = upstream.Write(challenge); e != nil {
			return e
		}
	}
	status := make([]byte, 4)
	if _, e := io.ReadFull(upstream, status); e != nil || binary.BigEndian.Uint32(status) != 0 {
		return fmt.Errorf("RFB authentication failed")
	}
	if _, e := client.Write([]byte("RFB 003.008\n")); e != nil {
		return e
	}
	if _, e := io.ReadFull(client, banner); e != nil || string(banner) != "RFB 003.008\n" {
		return fmt.Errorf("invalid client RFB version")
	}
	if _, e := client.Write([]byte{1, 1}); e != nil {
		return e
	}
	if _, e := io.ReadFull(client, n[:]); e != nil || n[0] != 1 {
		return fmt.Errorf("invalid client security")
	}
	_, e := client.Write([]byte{0, 0, 0, 0})
	return e
}
