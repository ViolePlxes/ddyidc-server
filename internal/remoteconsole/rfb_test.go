package remoteconsole

import (
	"bytes"
	"crypto/des"
	"io"
	"math/bits"
	"net"
	"testing"
)

func TestRFBServerSideAuthentication(t *testing.T) {
	up, server := net.Pipe()
	client, browser := net.Pipe()
	defer up.Close()
	defer server.Close()
	defer client.Close()
	defer browser.Close()
	done := make(chan error, 1)
	go func() { done <- authenticateRFB(up, client, "test-pass") }()
	srv := make(chan bool, 1)
	go func() {
		server.Write([]byte("RFB 003.008\n"))
		b := make([]byte, 12)
		io.ReadFull(server, b)
		server.Write([]byte{1, 2})
		choice := make([]byte, 1)
		io.ReadFull(server, choice)
		challenge := []byte("1234567890123456")
		server.Write(challenge)
		response := make([]byte, 16)
		io.ReadFull(server, response)
		key := []byte("test-pas")
		for i := range key {
			key[i] = bits.Reverse8(key[i])
		}
		cipher, _ := des.NewCipher(key)
		expected := make([]byte, 16)
		cipher.Encrypt(expected[:8], challenge[:8])
		cipher.Encrypt(expected[8:], challenge[8:])
		srv <- choice[0] == 2 && bytes.Equal(response, expected)
		server.Write([]byte{0, 0, 0, 0})
	}()
	b := make([]byte, 12)
	if _, e := io.ReadFull(browser, b); e != nil {
		t.Fatal(e)
	}
	browser.Write(b)
	sec := make([]byte, 2)
	io.ReadFull(browser, sec)
	if !bytes.Equal(sec, []byte{1, 1}) {
		t.Fatal("supplier secret authentication leaked to browser")
	}
	browser.Write([]byte{1})
	result := make([]byte, 4)
	io.ReadFull(browser, result)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if !<-srv {
		t.Fatal("wrong VNC challenge response")
	}
}
func TestRFBRejectsUnsupportedSecurity(t *testing.T) {
	up, server := net.Pipe()
	client, browser := net.Pipe()
	defer up.Close()
	defer server.Close()
	defer client.Close()
	defer browser.Close()
	done := make(chan error, 1)
	go func() { done <- authenticateRFB(up, client, "") }()
	server.Write([]byte("RFB 003.008\n"))
	io.ReadFull(server, make([]byte, 12))
	server.Write([]byte{1, 19})
	if e := <-done; e == nil {
		t.Fatal("unsupported security accepted")
	}
}
