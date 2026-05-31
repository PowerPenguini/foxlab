package wm

import (
	"context"
	"testing"
	"time"
)

func TestOpenWindowPublishesEvent(t *testing.T) {
	manager := NewManager()
	addr, err := manager.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := manager.Subscribe(ctx)

	err = OpenWindow(context.Background(), addr, OpenWindowRequest{
		AppID: "topology",
		Name:  "Topology Editor",
		Title: "Topology editor",
		Icon:  Icon{Type: "builtin", Value: "network"},
		Host:  "127.0.0.1",
		Port:  "49001",
		Path:  "/",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-events:
		if event.Type != "open-window" {
			t.Fatalf("unexpected event type %q", event.Type)
		}
		if event.AppID != "topology" || event.Name != "Topology Editor" || event.Title != "Topology editor" || event.Icon.Value != "network" || event.Host != "127.0.0.1" || event.Port != "49001" || event.Path != "/" {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for wm event")
	}
}

func TestCloseWindowPublishesEvent(t *testing.T) {
	manager := NewManager()
	addr, err := manager.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := manager.Subscribe(ctx)

	err = CloseWindow(context.Background(), addr, CloseWindowRequest{
		AppID: "terminal",
		Host:  "127.0.0.1",
		Port:  "49002",
		Path:  "/",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-events:
		if event.Type != "close-window" {
			t.Fatalf("unexpected event type %q", event.Type)
		}
		if event.AppID != "terminal" || event.Host != "127.0.0.1" || event.Port != "49002" || event.Path != "/" {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for wm event")
	}
}
