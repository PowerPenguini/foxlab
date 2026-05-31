package virt

import (
	"context"

	"foxlab/internal/lab"
)

type Driver interface {
	Apply(context.Context, *lab.Lab) error
	Destroy(context.Context, *lab.Lab) error
	Status(context.Context, *lab.Lab) (*LabStatus, error)
	Console(context.Context, *lab.Lab, string) (*ConsoleInfo, error)
	Close() error
}

type LabStatus struct {
	VMs           []VMStatus           `json:"vms"`
	Switches      []SwitchStatus       `json:"switches"`
	ExternalLinks []ExternalLinkStatus `json:"externalLinks"`
}

type VMStatus struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	State   string      `json:"state"`
	Console ConsoleInfo `json:"console,omitempty"`
}

type SwitchStatus struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type ExternalLinkStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Interface string `json:"interface"`
	State     string `json:"state"`
}

type ConsoleInfo struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
}
