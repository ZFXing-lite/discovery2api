package proxypool

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestReplaceSwapsLiveEntries(t *testing.T) {
	p := New([]struct {
		URL    string
		Weight int
	}{{URL: "socks5://127.0.0.1:1080", Weight: 1}}, "round-robin", time.Second)
	if p.Empty() || p.Pick("") == "" {
		t.Fatal("initial pool empty")
	}
	next := New([]struct {
		URL    string
		Weight int
	}{{URL: "socks5://10.0.0.8:1080", Weight: 1}}, "random", time.Second)
	p.Replace(next)
	got := p.Pick("")
	if got != "socks5://10.0.0.8:1080" {
		t.Fatalf("replace did not take effect: %s", got)
	}
	p.Replace(New(nil, "round-robin", time.Second))
	if !p.Empty() {
		t.Fatal("replace with empty pool should go direct")
	}
}

func TestHealthCheckMarksDeadProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	// A socks5 URL pointing at a closed port fails the dial.
	p := New([]struct {
		URL    string
		Weight int
	}{{URL: "socks5://127.0.0.1:1", Weight: 1}}, "round-robin", time.Minute)
	p.HealthCheck(context.Background(), ln.Addr().String())
	st := p.Status()
	if len(st) != 1 || st[0].Healthy || st[0].FailCount == 0 {
		t.Fatalf("dead proxy stayed healthy: %+v", st)
	}
}

func TestParseURL(t *testing.T) {
	if err := ParseURL("socks5://127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if err := ParseURL("http://127.0.0.1:8080"); err == nil {
		t.Fatal("http scheme must be rejected")
	}
}
