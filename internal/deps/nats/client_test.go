package nats

import (
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// TestIsClosed pins that a reconnecting connection is not closed, and that IsClosed
// answers while a reconnect attempt holds nats.go's connection lock.
func TestIsClosed(t *testing.T) {
	if !(&NATSClient{}).IsClosed() {
		t.Error("a client with no connection reported open")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The first connection is dropped so Connect fails fast and starts reconnecting; the
	// next one never speaks, so that attempt holds the lock until its handshake times out.
	stalled := make(chan net.Conn, 4)
	go func() {
		for first := true; ; first = false {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if first {
				_ = c.Close()
				continue
			}
			stalled <- c
		}
	}()

	conn, err := nats.Connect("nats://"+ln.Addr().String(), nats.RetryOnFailedConnect(true), nats.ReconnectWait(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)

	var c net.Conn
	select {
	case c = <-stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a reconnect attempt")
	}
	// Runs before conn.Close, which would otherwise wait out the stalled handshake.
	t.Cleanup(func() {
		_ = ln.Close()
		_ = c.Close()
	})

	client := &NATSClient{conn: conn}
	closed := make(chan bool, 1)
	go func() { closed <- client.IsClosed() }()
	select {
	case got := <-closed:
		if got {
			t.Error("a reconnecting connection reported closed")
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("IsClosed waited on the lock a reconnect attempt holds")
	}
}
