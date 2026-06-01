package lab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ManagedPrefix = "foxlab"
	FileExtension = ".lab"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type Lab struct {
	ID            string            `json:"id" yaml:"id"`
	VMs           []VM              `json:"vms,omitempty" yaml:"vms,omitempty"`
	Switches      []Switch          `json:"switches,omitempty" yaml:"switches,omitempty"`
	ExternalLinks []ExternalLink    `json:"externalLinks,omitempty" yaml:"externalLinks,omitempty"`
	Disks         []Disk            `json:"disks,omitempty" yaml:"disks,omitempty"`
	Layout        Layout            `json:"layout,omitempty" yaml:"layout,omitempty"`
	Meta          map[string]string `json:"meta,omitempty" yaml:"meta,omitempty"`

	path string
	root string
}

type VM struct {
	ID       string      `json:"id" yaml:"id"`
	Name     string      `json:"name,omitempty" yaml:"name,omitempty"`
	MemoryMB int         `json:"memoryMB" yaml:"memoryMB"`
	CPUs     int         `json:"cpus" yaml:"cpus"`
	Disk     string      `json:"disk" yaml:"disk"`
	ISO      string      `json:"iso,omitempty" yaml:"iso,omitempty"`
	VNC      bool        `json:"vnc,omitempty" yaml:"vnc,omitempty"`
	Networks []VMNetwork `json:"networks,omitempty" yaml:"networks,omitempty"`
}

type VMNetwork struct {
	Switch       string `json:"switch,omitempty" yaml:"switch,omitempty"`
	ExternalLink string `json:"externalLink,omitempty" yaml:"externalLink,omitempty"`
	MAC          string `json:"mac,omitempty" yaml:"mac,omitempty"`
}

type Switch struct {
	ID           string `json:"id" yaml:"id"`
	Name         string `json:"name,omitempty" yaml:"name,omitempty"`
	Mode         string `json:"mode" yaml:"mode"`
	ExternalLink string `json:"externalLink,omitempty" yaml:"externalLink,omitempty"`
}

type ExternalLink struct {
	ID        string `json:"id" yaml:"id"`
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	Interface string `json:"interface" yaml:"interface"`
}

type Disk struct {
	ID     string `json:"id" yaml:"id"`
	Path   string `json:"path" yaml:"path"`
	SizeGB int    `json:"sizeGB,omitempty" yaml:"sizeGB,omitempty"`
	Format string `json:"format,omitempty" yaml:"format,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

type Layout struct {
	Nodes map[string]Position `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	Links []LayoutLink        `json:"links,omitempty" yaml:"links,omitempty"`
}

type LayoutLink struct {
	From LayoutEndpoint `json:"from" yaml:"from"`
	To   LayoutEndpoint `json:"to" yaml:"to"`
}

type LayoutEndpoint struct {
	Type string `json:"type" yaml:"type"`
	ID   string `json:"id" yaml:"id"`
}

type Position struct {
	X int `json:"x" yaml:"x"`
	Y int `json:"y" yaml:"y"`
}

func LoadFile(path string) (*Lab, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var l Lab
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&l); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	l.path = abs
	l.root = filepath.Dir(abs)
	l.Normalize()
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

func SaveFile(path string, l *Lab) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	copy := *l
	copy.path = abs
	copy.root = filepath.Dir(abs)
	copy.Normalize()
	if err := copy.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&copy)
	if err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func ListFiles(workspace string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != workspace {
				return filepath.SkipDir
			}
			return nil
		}
		if isLabFile(path) {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func isLabFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == FileExtension
}

func (l *Lab) Normalize() {
	l.ID = strings.TrimSpace(l.ID)
	for i := range l.VMs {
		l.VMs[i].ID = strings.TrimSpace(l.VMs[i].ID)
		l.VMs[i].Name = strings.TrimSpace(l.VMs[i].Name)
		l.VMs[i].Disk = strings.TrimSpace(l.VMs[i].Disk)
		l.VMs[i].ISO = strings.TrimSpace(l.VMs[i].ISO)
		for j := range l.VMs[i].Networks {
			l.VMs[i].Networks[j].Switch = strings.TrimSpace(l.VMs[i].Networks[j].Switch)
			l.VMs[i].Networks[j].ExternalLink = strings.TrimSpace(l.VMs[i].Networks[j].ExternalLink)
			l.VMs[i].Networks[j].MAC = strings.TrimSpace(l.VMs[i].Networks[j].MAC)
		}
		if l.VMs[i].MemoryMB == 0 {
			l.VMs[i].MemoryMB = 2048
		}
		if l.VMs[i].CPUs == 0 {
			l.VMs[i].CPUs = 2
		}
	}
	for i := range l.Switches {
		l.Switches[i].ID = strings.TrimSpace(l.Switches[i].ID)
		l.Switches[i].Name = strings.TrimSpace(l.Switches[i].Name)
		l.Switches[i].Mode = strings.TrimSpace(l.Switches[i].Mode)
		l.Switches[i].ExternalLink = strings.TrimSpace(l.Switches[i].ExternalLink)
		if l.Switches[i].ExternalLink != "" && l.Switches[i].Mode == "" {
			l.Switches[i].Mode = "bridge"
		}
		if l.Switches[i].Mode == "" {
			l.Switches[i].Mode = "bridge"
		}
	}
	for i := range l.ExternalLinks {
		l.ExternalLinks[i].ID = strings.TrimSpace(l.ExternalLinks[i].ID)
		l.ExternalLinks[i].Name = strings.TrimSpace(l.ExternalLinks[i].Name)
		l.ExternalLinks[i].Interface = strings.TrimSpace(l.ExternalLinks[i].Interface)
	}
	for i := range l.Disks {
		l.Disks[i].ID = strings.TrimSpace(l.Disks[i].ID)
		l.Disks[i].Path = strings.TrimSpace(l.Disks[i].Path)
		l.Disks[i].Source = strings.TrimSpace(l.Disks[i].Source)
		l.Disks[i].Format = strings.TrimSpace(strings.ToLower(l.Disks[i].Format))
		if l.Disks[i].Format == "" {
			l.Disks[i].Format = "qcow2"
		}
	}
}

func (l *Lab) Validate() error {
	var problems []string
	if !validID(l.ID) {
		problems = append(problems, "lab id must start with a letter/number and contain only letters, numbers, '_' or '-'")
	}

	switchIDs := map[string]struct{}{}
	for _, sw := range l.Switches {
		if !validID(sw.ID) {
			problems = append(problems, fmt.Sprintf("switch %q has invalid id", sw.ID))
		}
		if _, exists := switchIDs[sw.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate switch id %q", sw.ID))
		}
		switchIDs[sw.ID] = struct{}{}
		if sw.Mode != "bridge" && sw.Mode != "nat" && sw.Mode != "macnat-bridge" {
			problems = append(problems, fmt.Sprintf("switch %q uses unsupported mode %q; supported modes are bridge, nat and macnat-bridge", sw.ID, sw.Mode))
		}
		if sw.Mode == "macnat-bridge" && sw.ExternalLink == "" {
			problems = append(problems, fmt.Sprintf("switch %q macnat-bridge mode requires externalLink", sw.ID))
		}
	}

	externalLinkIDs := map[string]struct{}{}
	for _, link := range l.ExternalLinks {
		if !validID(link.ID) {
			problems = append(problems, fmt.Sprintf("external link %q has invalid id", link.ID))
		}
		if _, exists := externalLinkIDs[link.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate external link id %q", link.ID))
		}
		externalLinkIDs[link.ID] = struct{}{}
		if link.Interface == "" {
			problems = append(problems, fmt.Sprintf("external link %q interface is required", link.ID))
		}
	}

	for _, sw := range l.Switches {
		if sw.ExternalLink == "" {
			continue
		}
		if _, ok := externalLinkIDs[sw.ExternalLink]; !ok {
			problems = append(problems, fmt.Sprintf("switch %q references missing external link %q", sw.ID, sw.ExternalLink))
		}
	}

	diskIDs := map[string]struct{}{}
	diskPaths := map[string]struct{}{}
	for _, d := range l.Disks {
		if !validID(d.ID) {
			problems = append(problems, fmt.Sprintf("disk %q has invalid id", d.ID))
		}
		if _, exists := diskIDs[d.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate disk id %q", d.ID))
		}
		diskIDs[d.ID] = struct{}{}
		if d.Path == "" {
			problems = append(problems, fmt.Sprintf("disk %q path is required", d.ID))
		}
		if filepath.IsAbs(d.Path) {
			problems = append(problems, fmt.Sprintf("disk %q path must be relative to the lab workspace", d.ID))
		}
		cleanPath := filepath.Clean(d.Path)
		if _, exists := diskPaths[cleanPath]; exists {
			problems = append(problems, fmt.Sprintf("duplicate disk path %q", d.Path))
		}
		diskPaths[cleanPath] = struct{}{}
		if d.SizeGB < 0 {
			problems = append(problems, fmt.Sprintf("disk %q sizeGB cannot be negative", d.ID))
		}
		if d.SizeGB == 0 && d.Source == "" {
			problems = append(problems, fmt.Sprintf("disk %q needs sizeGB or source", d.ID))
		}
		if d.Format != "qcow2" && d.Format != "raw" {
			problems = append(problems, fmt.Sprintf("disk %q format must be qcow2 or raw", d.ID))
		}
	}

	vmIDs := map[string]struct{}{}
	for _, vm := range l.VMs {
		if !validID(vm.ID) {
			problems = append(problems, fmt.Sprintf("vm %q has invalid id", vm.ID))
		}
		if _, exists := vmIDs[vm.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate vm id %q", vm.ID))
		}
		vmIDs[vm.ID] = struct{}{}
		if vm.MemoryMB <= 0 {
			problems = append(problems, fmt.Sprintf("vm %q memoryMB must be greater than zero", vm.ID))
		}
		if vm.CPUs <= 0 {
			problems = append(problems, fmt.Sprintf("vm %q cpus must be greater than zero", vm.ID))
		}
		if vm.Disk == "" {
			problems = append(problems, fmt.Sprintf("vm %q disk is required", vm.ID))
		}
		for _, nic := range vm.Networks {
			switchRef := nic.Switch != ""
			externalRef := nic.ExternalLink != ""
			if switchRef == externalRef {
				problems = append(problems, fmt.Sprintf("vm %q network must reference exactly one switch or externalLink", vm.ID))
				continue
			}
			if switchRef {
				if _, ok := switchIDs[nic.Switch]; !ok {
					problems = append(problems, fmt.Sprintf("vm %q references missing switch %q", vm.ID, nic.Switch))
				}
			}
			if externalRef {
				if _, ok := externalLinkIDs[nic.ExternalLink]; !ok {
					problems = append(problems, fmt.Sprintf("vm %q references missing external link %q", vm.ID, nic.ExternalLink))
				}
			}
		}
	}

	for id := range l.Layout.Nodes {
		if _, ok := vmIDs[id]; ok {
			continue
		}
		if _, ok := switchIDs[id]; ok {
			continue
		}
		if _, ok := externalLinkIDs[id]; ok {
			continue
		}
		problems = append(problems, fmt.Sprintf("layout references missing node %q", id))
	}
	for _, link := range l.Layout.Links {
		for _, endpoint := range []LayoutEndpoint{link.From, link.To} {
			switch endpoint.Type {
			case "vm":
				if _, ok := vmIDs[endpoint.ID]; !ok {
					problems = append(problems, fmt.Sprintf("layout link references missing vm %q", endpoint.ID))
				}
			case "switch":
				if _, ok := switchIDs[endpoint.ID]; !ok {
					problems = append(problems, fmt.Sprintf("layout link references missing switch %q", endpoint.ID))
				}
			case "external":
				if _, ok := externalLinkIDs[endpoint.ID]; !ok {
					problems = append(problems, fmt.Sprintf("layout link references missing external link %q", endpoint.ID))
				}
			default:
				problems = append(problems, fmt.Sprintf("layout link references unknown node type %q", endpoint.Type))
			}
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (l *Lab) Path() string {
	return l.path
}

func (l *Lab) Root() string {
	if l.root != "" {
		return l.root
	}
	return "."
}

func (l *Lab) DiskBaseDir() string {
	return filepath.Join(l.Root(), "labs", l.ID, "disks")
}

func (l *Lab) ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(l.Root(), path))
}

func (l *Lab) ResolveDiskPath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(l.Root(), path))
}

func (l *Lab) ManagedDomainName(vm VM) string {
	return managedName(l.ID, vm.ID)
}

func (l *Lab) ManagedNetworkName(sw Switch) string {
	return managedName(l.ID, sw.ID)
}

func (l *Lab) DiskByPath(path string) (Disk, bool) {
	clean := filepath.Clean(path)
	for _, d := range l.Disks {
		if filepath.Clean(d.Path) == clean {
			return d, true
		}
	}
	return Disk{}, false
}

func validID(id string) bool {
	return idPattern.MatchString(id)
}

func managedName(labID, resourceID string) string {
	return strings.ToLower(fmt.Sprintf("%s-%s-%s", ManagedPrefix, labID, resourceID))
}
