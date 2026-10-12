package server

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// Shutdown with a request still being read must wait out its grace and
// return, not panic. It passed a nil context to http.Server.Shutdown,
// which is dereferenced only when there is something to wait for — so it
// failed only when a connection happened to be active.
func TestShutdownWithARequestInFlight(t *testing.T) {
	saved := shutdownGrace
	shutdownGrace = 100 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = saved })

	srv := New(broker.New(), broker.NewAgentRegistry())
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	// Half a request: the connection is active, not idle.
	conn, err := net.Dial("tcp", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /api/v1/monitor/x HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}
