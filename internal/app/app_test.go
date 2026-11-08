package app

import (
	"context"
	"net"
	"testing"
	"time"

	"iptv-steady/internal/testkit"
)

func TestServeStopsWhenThePortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	cfg := testkit.Config(t, testkit.NewPanel(t))
	cfg.Server.Listen = taken.Addr().String()
	instance, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()

	finished := make(chan error, 1)
	go func() { finished <- instance.Serve(context.Background()) }()

	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("serving without a listener must be an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the process kept running without a listener")
	}
}
