package notify

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestSMTPContextStopsWaitingForGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var one [1]byte
		_, _ = conn.Read(one[:])
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = (smtpMailChannel{}).Send(ctx, MailConfig{Host: host, Port: port}, "test@example.com", "test", "test")
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("SMTP greeting ignored context cancellation")
	}
	<-done
}
