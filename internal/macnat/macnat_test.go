package macnat

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestAvailableReportsMissingDevice(t *testing.T) {
	ctrl := NewController("/tmp/foxlab-macnat-missing-device")
	err := ctrl.Available()
	if err == nil || !strings.Contains(err.Error(), "kernel module") {
		t.Fatalf("expected missing module guidance, got %v", err)
	}
}

func TestClearIgnoresMissingDevice(t *testing.T) {
	ctrl := NewController("/tmp/foxlab-macnat-missing-device")
	if err := ctrl.Clear(context.Background(), "lab-1"); err != nil {
		t.Fatalf("clear should ignore missing device, got %v", err)
	}
}

func TestNeedsController(t *testing.T) {
	if !NeedsController("macnat-bridge") {
		t.Fatal("macnat-bridge should require the controller")
	}
	if NeedsController("bridge") || NeedsController("nat") {
		t.Fatal("ordinary switch modes should not require the controller")
	}
}

func TestStatusReadsDriverReport(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"state":"active","message":"packet path active","switchID":"sw1","bridge":"flsw1","uplink":"wlp0s20f3","vmCount":2,"txToUplink":7,"rxToVM":5,"drops":1,"learnedPorts":2,"learnedIPv4":2,"learnedDHCP":1}`)
	_ = f.Close()

	state, detail := NewController(f.Name()).Status()
	if state != "active" {
		t.Fatalf("expected active status, got %q (%s)", state, detail)
	}
	for _, want := range []string{"packet path active", "switch=sw1", "tx=7", "rx=5", "drops=1"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expected detail to contain %q, got %q", want, detail)
		}
	}
}

func TestStatusForSwitchReadsMultiSessionDriverReport(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"state":"active","message":"packet path active","sessionCount":2,"sessions":[{"state":"active","message":"packet path active","switchID":"sw1","bridge":"flsw1","uplink":"wlp0s20f3","vmCount":1,"txToUplink":3},{"state":"active","message":"packet path active","switchID":"sw2","bridge":"flsw2","uplink":"wlp0s20f3","vmCount":2,"rxToVM":4}]}`)
	_ = f.Close()

	state, detail := NewController(f.Name()).StatusForSwitch("sw2")
	if state != "active" {
		t.Fatalf("expected active status, got %q (%s)", state, detail)
	}
	for _, want := range []string{"switch=sw2", "bridge=flsw2", "vms=2", "rx=4"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expected detail to contain %q, got %q", want, detail)
		}
	}
}

func TestConfigureCommandRejectsWhitespace(t *testing.T) {
	_, err := configureCommand(Session{
		LabID:    "bad lab",
		SwitchID: "sw1",
		Bridge:   "flsw1",
		Uplink:   "wlp0s20f3",
	})
	if err == nil {
		t.Fatal("expected whitespace in command value to be rejected")
	}
}

func TestConfigureCommandsClearsLabThenConfiguresEachSession(t *testing.T) {
	commands, err := configureCommands([]Session{
		{LabID: "lab-1", SwitchID: "sw1", Bridge: "flsw1", Uplink: "wlp0s20f3", VMMACs: []string{"52:54:00:aa:bb:01"}},
		{LabID: "lab-1", SwitchID: "sw2", Bridge: "flsw2", Uplink: "wlp0s20f3", VMMACs: []string{"52:54:00:aa:bb:02"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(commands))
	for _, command := range commands {
		got = append(got, string(command))
	}
	want := []string{
		"clear labID=lab-1\n",
		"configure labID=lab-1 switchID=sw1 bridge=flsw1 uplink=wlp0s20f3 mac=52:54:00:aa:bb:01\n",
		"configure labID=lab-1 switchID=sw2 bridge=flsw2 uplink=wlp0s20f3 mac=52:54:00:aa:bb:02\n",
	}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Fatalf("unexpected commands:\n%q", got)
	}
}
