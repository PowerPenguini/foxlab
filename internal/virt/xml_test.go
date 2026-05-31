package virt

import (
	"strings"
	"testing"

	"foxlab/internal/lab"
	"foxlab/internal/macnat"
)

func TestDomainXMLUsesNetworkInterfaces(t *testing.T) {
	l := &lab.Lab{
		ID:       "demo",
		Switches: []lab.Switch{{ID: "sw1", Mode: "bridge", ExternalLink: "uplink1"}},
	}
	vm := lab.VM{ID: "vm1", MemoryMB: 512, CPUs: 1, Disk: "disks/vm1.qcow2", VNC: true, Networks: []lab.VMNetwork{{Switch: "sw1"}}}
	xmlText, err := domainXML(l, vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<name>foxlab-demo-vm1</name>`, `source network="foxlab-demo-sw1"`, `type="vnc"`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("domain XML missing %q:\n%s", want, xmlText)
		}
	}
}

func TestDomainXMLUsesBridgeInterfaceForExternalLink(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "br0"}},
	}
	vm := lab.VM{
		ID:       "vm1",
		MemoryMB: 512,
		CPUs:     1,
		Disk:     "disks/vm1.qcow2",
		Networks: []lab.VMNetwork{{ExternalLink: "uplink1", MAC: "52:54:00:12:34:56"}},
	}
	xmlText, err := domainXML(l, vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<interface type="bridge">`, `source bridge="br0"`, `mac address="52:54:00:12:34:56"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("domain XML missing %q:\n%s", want, xmlText)
		}
	}
}

func TestDomainXMLUsesDirectInterfaceForEthernetExternalLink(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "eth0"}},
	}
	vm := lab.VM{
		ID:       "vm1",
		MemoryMB: 512,
		CPUs:     1,
		Disk:     "disks/vm1.qcow2",
		Networks: []lab.VMNetwork{{ExternalLink: "uplink1"}},
	}
	xmlText, err := domainXML(l, vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<interface type="direct">`, `source dev="eth0" mode="bridge"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("domain XML missing %q:\n%s", want, xmlText)
		}
	}
}

func TestDomainXMLUsesDirectInterfaceForWirelessExternalLink(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "wlp0s20f3"}},
	}
	vm := lab.VM{
		ID:       "vm1",
		MemoryMB: 512,
		CPUs:     1,
		Disk:     "disks/vm1.qcow2",
		Networks: []lab.VMNetwork{{ExternalLink: "uplink1"}},
	}
	xmlText, err := domainXML(l, vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<interface type="direct">`, `source dev="wlp0s20f3" mode="bridge"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("domain XML missing %q:\n%s", want, xmlText)
		}
	}
}

func TestDomainXMLBootsFromISOWhenConfigured(t *testing.T) {
	l := &lab.Lab{ID: "demo"}
	vm := lab.VM{ID: "vm1", MemoryMB: 512, CPUs: 1, Disk: "disks/vm1.qcow2", ISO: "/home/user/Downloads/alpine.iso"}
	xmlText, err := domainXML(l, vm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<boot dev="cdrom"/>`, `<source file="/home/user/Downloads/alpine.iso"/>`, `<readonly/>`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("domain XML missing %q:\n%s", want, xmlText)
		}
	}
}

func TestNetworkXMLUsesLocalBridgeWithoutExternalLink(t *testing.T) {
	l := &lab.Lab{ID: "demo"}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<bridge name="flfoxlabdemosw1" stp="on" delay="0"/>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, "<forward") {
		t.Fatalf("bridge switch without uplink must not contain forward mode:\n%s", xmlText)
	}
}

func TestNetworkXMLUsesExistingHostBridge(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "br0"}},
	}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "bridge", ExternalLink: "uplink1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<forward mode="bridge"/>`, `<bridge name="br0"/>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, `stp="on"`) {
		t.Fatalf("bridge-backed network must not configure host bridge management attributes:\n%s", xmlText)
	}
}

func TestNetworkXMLUsesEthernetUplinkInterface(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "eth0"}},
	}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "bridge", ExternalLink: "uplink1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<forward mode="bridge">`, `<interface dev="eth0"/>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, `<bridge name="eth0"`) {
		t.Fatalf("physical uplink must not be emitted as a bridge name:\n%s", xmlText)
	}
}

func TestNetworkXMLUsesWirelessUplinkInterface(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "wlp0s20f3"}},
	}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "bridge", ExternalLink: "uplink1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<forward mode="bridge">`, `<interface dev="wlp0s20f3"/>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, `<forward mode="nat"`) || strings.Contains(xmlText, `<dhcp>`) {
		t.Fatalf("wireless uplink must not silently fall back to NAT/DHCP:\n%s", xmlText)
	}
}

func TestNetworkXMLUsesNATWhenSwitchModeIsNAT(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "wlp0s20f3"}},
	}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "nat", ExternalLink: "uplink1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<forward mode="nat" dev="wlp0s20f3"/>`, `<dhcp>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, `<forward mode="bridge"`) {
		t.Fatalf("NAT switch must not use passthrough bridge mode:\n%s", xmlText)
	}
}

func TestNetworkXMLUsesNATWithoutExternalLink(t *testing.T) {
	l := &lab.Lab{ID: "demo"}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "nat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<forward mode="nat"/>`, `<dhcp>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, `dev="`) {
		t.Fatalf("NAT switch without uplink must not pin a host interface:\n%s", xmlText)
	}
}

func TestNetworkXMLUsesLocalBridgeForMACNATBridgeMode(t *testing.T) {
	l := &lab.Lab{
		ID:            "demo",
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Interface: "wlp0s20f3"}},
	}
	xmlText, err := networkXML(l, lab.Switch{ID: "sw1", Mode: "macnat-bridge", ExternalLink: "uplink1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<bridge name="flfoxlabdemosw1" stp="on" delay="0"/>`, `lab="demo"`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("network XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, "<forward") || strings.Contains(xmlText, "<dhcp>") {
		t.Fatalf("macnat-bridge libvirt network must be local only:\n%s", xmlText)
	}
}

func TestParseDomainNetworkMACs(t *testing.T) {
	xmlText := `<domain><devices>
<interface type="network"><mac address="52:54:00:aa:bb:01"/><source network="foxlab-demo-sw1"/></interface>
<interface type="network"><mac address="52:54:00:aa:bb:02"/><source network="foxlab-demo-sw2"/></interface>
<interface type="network"><mac address="52:54:00:aa:bb:03"/><source network="foxlab-demo-sw1"/></interface>
</devices></domain>`
	got := parseDomainNetworkMACs(xmlText, "foxlab-demo-sw1")
	if len(got) != 2 || got[0] != "52:54:00:aa:bb:01" || got[1] != "52:54:00:aa:bb:03" {
		t.Fatalf("unexpected network MACs: %#v", got)
	}
}

func TestCollectMacNATMACs(t *testing.T) {
	l := &lab.Lab{
		ID:       "demo",
		Switches: []lab.Switch{{ID: "sw1", Mode: "macnat-bridge", ExternalLink: "uplink1"}},
	}
	vm := lab.VM{ID: "vm1", Networks: []lab.VMNetwork{{Switch: "sw1"}}}
	xmlText := `<domain><devices>
<interface type="network"><mac address="52:54:00:aa:bb:01"/><source network="foxlab-demo-sw1"/></interface>
</devices></domain>`
	sessions := []macnat.Session{{LabID: "demo", SwitchID: "sw1", Bridge: "flfoxlabdemosw1", Uplink: "wlp0s20f3"}}
	collectMacNATMACs(l, vm, xmlText, sessions)
	if len(sessions[0].VMMACs) != 1 || sessions[0].VMMACs[0] != "52:54:00:aa:bb:01" {
		t.Fatalf("unexpected session MACs: %#v", sessions[0].VMMACs)
	}
}
