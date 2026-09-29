package relay

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestConnectTimeoutKeepsProxyDialer(t *testing.T) {
	var dialed bool
	base := &http.Transport{}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = true
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("connect timeout was not applied to the proxy dialer")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}
	wrapped := withConnectTimeout(base, 2*time.Second).(*http.Transport)
	conn, err := wrapped.DialContext(context.Background(), "tcp", "example.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if !dialed {
		t.Fatal("original dialer was replaced")
	}
}
