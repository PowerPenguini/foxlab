package virt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	libvirt "github.com/libvirt/libvirt-go"

	"foxlab/internal/lab"
	"foxlab/internal/macnat"
)

type LibvirtDriver struct {
	conn *libvirt.Connect
}

func NewLibvirtDriver(uri string) (*LibvirtDriver, error) {
	conn, err := libvirt.NewConnect(uri)
	if err != nil {
		return nil, err
	}
	return &LibvirtDriver{conn: conn}, nil
}

func (d *LibvirtDriver) Close() error {
	if d.conn == nil {
		return nil
	}
	_, err := d.conn.Close()
	return err
}

func (d *LibvirtDriver) Apply(ctx context.Context, l *lab.Lab) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	macnatSessions, err := d.prepareMacNATSessions(l)
	if err != nil {
		return err
	}
	macnatController := macnat.NewController("")
	if len(macnatSessions) > 0 {
		if err := macnatController.Available(); err != nil {
			return err
		}
	}
	if err := d.removeLabDomainsIfManaged(l); err != nil {
		return err
	}
	if err := d.removeLabNetworksIfManaged(l); err != nil {
		return err
	}
	for _, sw := range l.Switches {
		if err := d.reconcileNetwork(l, sw); err != nil {
			return err
		}
	}
	for _, vm := range l.VMs {
		if err := ctx.Err(); err != nil {
			return err
		}
		xmlText, err := domainXML(l, vm)
		if err != nil {
			return err
		}
		dom, err := d.conn.DomainDefineXML(xmlText)
		if err != nil {
			return fmt.Errorf("define domain %q: %w", vm.ID, err)
		}
		if err := dom.Create(); err != nil {
			_ = dom.Undefine()
			return fmt.Errorf("start domain %q: %w", vm.ID, err)
		}
		if len(macnatSessions) > 0 {
			xmlText, xmlErr := dom.GetXMLDesc(0)
			if xmlErr != nil {
				_ = dom.Free()
				return xmlErr
			}
			collectMacNATMACs(l, vm, xmlText, macnatSessions)
		}
		_ = dom.Free()
	}
	if err := macnatController.Configure(ctx, macnatSessions); err != nil {
		return err
	}
	return nil
}

func (d *LibvirtDriver) Destroy(ctx context.Context, l *lab.Lab) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.removeLabDomainsIfManaged(l); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.removeLabNetworksIfManaged(l); err != nil {
		return err
	}
	if err := macnat.NewController("").Clear(ctx, l.ID); err != nil {
		return err
	}
	return nil
}

func (d *LibvirtDriver) Status(ctx context.Context, l *lab.Lab) (*LabStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := &LabStatus{}
	for _, sw := range l.Switches {
		st := SwitchStatus{ID: sw.ID, Name: l.ManagedNetworkName(sw), State: "missing"}
		net, err := d.conn.LookupNetworkByName(l.ManagedNetworkName(sw))
		if err == nil {
			active, activeErr := net.IsActive()
			if activeErr != nil {
				st.State = "unknown"
			} else if active {
				st.State = "active"
			} else {
				st.State = "inactive"
			}
			_ = net.Free()
		} else if !isNotFound(err) {
			return nil, err
		}
		if sw.Mode == macnat.Mode {
			wifiState, detail := macnat.NewController("").StatusForSwitch(sw.ID)
			st.Detail = detail
			if st.State == "active" && wifiState != "active" {
				st.State = wifiState
			}
		}
		out.Switches = append(out.Switches, st)
	}
	for _, link := range l.ExternalLinks {
		out.ExternalLinks = append(out.ExternalLinks, externalLinkStatus(link))
	}
	for _, vm := range l.VMs {
		st := VMStatus{ID: vm.ID, Name: l.ManagedDomainName(vm), State: "missing"}
		dom, err := d.conn.LookupDomainByName(l.ManagedDomainName(vm))
		if err == nil {
			state, _, stateErr := dom.GetState()
			if stateErr != nil {
				st.State = "unknown"
			} else {
				st.State = domainStateName(state)
			}
			if vm.VNC {
				if xmlText, xmlErr := dom.GetXMLDesc(0); xmlErr == nil {
					if port := parseVNCPort(xmlText); port > 0 {
						st.Console = ConsoleInfo{Enabled: true, Host: "127.0.0.1", Port: port}
					}
				}
			}
			_ = dom.Free()
		} else if !isNotFound(err) {
			return nil, err
		}
		out.VMs = append(out.VMs, st)
	}
	return out, nil
}

func (d *LibvirtDriver) prepareMacNATSessions(l *lab.Lab) ([]macnat.Session, error) {
	var sessions []macnat.Session
	for _, sw := range l.Switches {
		if !macnat.NeedsController(sw.Mode) {
			continue
		}
		if sw.ExternalLink == "" {
			return nil, fmt.Errorf("switch %q macnat-bridge mode requires externalLink", sw.ID)
		}
		link, ok := findExternalLink(l, sw.ExternalLink)
		if !ok {
			return nil, fmt.Errorf("switch %q references missing external link %q", sw.ID, sw.ExternalLink)
		}
		if _, err := net.InterfaceByName(link.Interface); err != nil {
			return nil, fmt.Errorf("macnat uplink %q for switch %q is not present: %w", link.Interface, sw.ID, err)
		}
		sessions = append(sessions, macnat.Session{
			LabID:    l.ID,
			SwitchID: sw.ID,
			Bridge:   bridgeName(l.ManagedNetworkName(sw)),
			Uplink:   link.Interface,
		})
	}
	return sessions, nil
}

func collectMacNATMACs(l *lab.Lab, vm lab.VM, xmlText string, sessions []macnat.Session) {
	if len(sessions) == 0 {
		return
	}
	for _, nic := range vm.Networks {
		if nic.Switch == "" {
			continue
		}
		sw, ok := findSwitch(l, nic.Switch)
		if !ok || !macnat.NeedsController(sw.Mode) {
			continue
		}
		networkName := l.ManagedNetworkName(sw)
		macs := parseDomainNetworkMACs(xmlText, networkName)
		for i := range sessions {
			if sessions[i].SwitchID == sw.ID {
				sessions[i].VMMACs = appendUniqueStrings(sessions[i].VMMACs, macs...)
			}
		}
	}
}

func appendUniqueStrings(items []string, next ...string) []string {
	seen := make(map[string]struct{}, len(items)+len(next))
	for _, item := range items {
		seen[item] = struct{}{}
	}
	for _, item := range next {
		if _, ok := seen[item]; ok {
			continue
		}
		items = append(items, item)
		seen[item] = struct{}{}
	}
	return items
}

func externalLinkStatus(link lab.ExternalLink) ExternalLinkStatus {
	st := ExternalLinkStatus{
		ID:        link.ID,
		Name:      link.Name,
		Interface: link.Interface,
		State:     "missing",
	}
	if link.Interface == "" {
		return st
	}
	iface, err := net.InterfaceByName(link.Interface)
	if err != nil {
		return st
	}
	if iface.Flags&net.FlagUp != 0 {
		st.State = "up"
	} else {
		st.State = "down"
	}
	return st
}

func (d *LibvirtDriver) Console(ctx context.Context, l *lab.Lab, vmID string) (*ConsoleInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, vm := range l.VMs {
		if vm.ID != vmID {
			continue
		}
		if !vm.VNC {
			return &ConsoleInfo{Enabled: false}, nil
		}
		dom, err := d.conn.LookupDomainByName(l.ManagedDomainName(vm))
		if err != nil {
			return nil, err
		}
		defer dom.Free()
		xmlText, err := dom.GetXMLDesc(0)
		if err != nil {
			return nil, err
		}
		port := parseVNCPort(xmlText)
		if port <= 0 {
			return &ConsoleInfo{Enabled: true, Host: "127.0.0.1"}, nil
		}
		return &ConsoleInfo{Enabled: true, Host: "127.0.0.1", Port: port}, nil
	}
	return nil, fmt.Errorf("unknown vm %q", vmID)
}

func (d *LibvirtDriver) reconcileNetwork(l *lab.Lab, sw lab.Switch) error {
	name := l.ManagedNetworkName(sw)
	if err := d.removeNetworkIfManaged(l, name); err != nil {
		return err
	}
	xmlText, err := networkXML(l, sw)
	if err != nil {
		return err
	}
	net, err := d.conn.NetworkDefineXML(xmlText)
	if err != nil {
		return fmt.Errorf("define network %q: %w", sw.ID, err)
	}
	if err := net.Create(); err != nil {
		_ = net.Undefine()
		return fmt.Errorf("start network %q: %w", sw.ID, err)
	}
	_ = net.Free()
	return nil
}

func (d *LibvirtDriver) removeDomainIfManaged(l *lab.Lab, name string) error {
	dom, err := d.conn.LookupDomainByName(name)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	defer dom.Free()
	xmlText, err := dom.GetXMLDesc(0)
	if err != nil {
		return err
	}
	if !isManagedXML(xmlText, l.ID) {
		return fmt.Errorf("domain %q already exists and is not managed by lab %q", name, l.ID)
	}
	return removeManagedDomain(dom)
}

func (d *LibvirtDriver) removeLabDomainsIfManaged(l *lab.Lab) error {
	doms, err := d.conn.ListAllDomains(0)
	if err != nil {
		return err
	}
	for i := range doms {
		dom := &doms[i]
		xmlText, xmlErr := dom.GetXMLDesc(0)
		if xmlErr != nil {
			_ = dom.Free()
			return xmlErr
		}
		if !isManagedXML(xmlText, l.ID) {
			_ = dom.Free()
			continue
		}
		if err := removeManagedDomain(dom); err != nil {
			_ = dom.Free()
			return err
		}
		_ = dom.Free()
	}
	return nil
}

func removeManagedDomain(dom *libvirt.Domain) error {
	state, _, err := dom.GetState()
	if err == nil && state != libvirt.DOMAIN_SHUTOFF {
		if err := dom.Destroy(); err != nil && !isNotFound(err) {
			return err
		}
	}
	if err := dom.Undefine(); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (d *LibvirtDriver) removeNetworkIfManaged(l *lab.Lab, name string) error {
	net, err := d.conn.LookupNetworkByName(name)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	defer net.Free()
	xmlText, err := net.GetXMLDesc(0)
	if err != nil {
		return err
	}
	if !isManagedXML(xmlText, l.ID) {
		return fmt.Errorf("network %q already exists and is not managed by lab %q", name, l.ID)
	}
	return removeManagedNetwork(net)
}

func (d *LibvirtDriver) removeLabNetworksIfManaged(l *lab.Lab) error {
	nets, err := d.conn.ListAllNetworks(0)
	if err != nil {
		return err
	}
	for i := range nets {
		net := &nets[i]
		xmlText, xmlErr := net.GetXMLDesc(0)
		if xmlErr != nil {
			_ = net.Free()
			return xmlErr
		}
		if !isManagedXML(xmlText, l.ID) {
			_ = net.Free()
			continue
		}
		if err := removeManagedNetwork(net); err != nil {
			_ = net.Free()
			return err
		}
		_ = net.Free()
	}
	return nil
}

func removeManagedNetwork(net *libvirt.Network) error {
	active, err := net.IsActive()
	if err == nil && active {
		if err := net.Destroy(); err != nil && !isNotFound(err) {
			return err
		}
	}
	if err := net.Undefine(); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not found") ||
		strings.Contains(text, "no domain") ||
		strings.Contains(text, "no network") ||
		errors.Is(err, osErrNotExist{})
}

type osErrNotExist struct{}

func (osErrNotExist) Error() string { return "not exist" }

func domainStateName(state libvirt.DomainState) string {
	switch state {
	case libvirt.DOMAIN_RUNNING:
		return "running"
	case libvirt.DOMAIN_BLOCKED:
		return "blocked"
	case libvirt.DOMAIN_PAUSED:
		return "paused"
	case libvirt.DOMAIN_SHUTDOWN:
		return "shutdown"
	case libvirt.DOMAIN_SHUTOFF:
		return "shutoff"
	case libvirt.DOMAIN_CRASHED:
		return "crashed"
	case libvirt.DOMAIN_PMSUSPENDED:
		return "suspended"
	default:
		return "unknown"
	}
}
