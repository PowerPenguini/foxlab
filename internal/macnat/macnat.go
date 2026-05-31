package macnat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

const (
	Mode              = "macnat-bridge"
	DefaultDevicePath = "/dev/macnat"
	ModuleName        = "foxlab_macnat"
)

type Controller struct {
	DevicePath string
}

type Session struct {
	LabID    string   `json:"labID"`
	SwitchID string   `json:"switchID"`
	Bridge   string   `json:"bridge"`
	Uplink   string   `json:"uplink"`
	VMMACs   []string `json:"vmMACs"`
}

type driverSessionStatus struct {
	State                string `json:"state"`
	Message              string `json:"message"`
	LabID                string `json:"labID"`
	SwitchID             string `json:"switchID"`
	Bridge               string `json:"bridge"`
	Uplink               string `json:"uplink"`
	VMCount              int    `json:"vmCount"`
	TxToUplink           uint64 `json:"txToUplink"`
	RxToVM               uint64 `json:"rxToVM"`
	Drops                uint64 `json:"drops"`
	LearnedPorts         uint64 `json:"learnedPorts"`
	LearnedIPv4          uint64 `json:"learnedIPv4"`
	LearnedDHCP          uint64 `json:"learnedDHCP"`
	IgnoredWrongBridge   uint64 `json:"ignoredWrongBridge"`
	DHCPOption61Rewrite  uint64 `json:"dhcpOption61Rewrite"`
	DHCPChecksumRewrite  uint64 `json:"dhcpChecksumRewrite"`
	InboundNoVMMatch     uint64 `json:"inboundNoVMMatch"`
	InboundNoLearnedPort uint64 `json:"inboundNoLearnedPort"`
}

type driverStatus struct {
	State           string                `json:"state"`
	Message         string                `json:"message"`
	SessionCount    int                   `json:"sessionCount"`
	Sessions        []driverSessionStatus `json:"sessions"`
	LastConfigBytes int                   `json:"lastConfigBytes"`

	// Legacy single-session fields are kept so older loaded modules still
	// produce a useful status while the new module is being rolled out.
	LabID                string `json:"labID"`
	SwitchID             string `json:"switchID"`
	Bridge               string `json:"bridge"`
	Uplink               string `json:"uplink"`
	VMCount              int    `json:"vmCount"`
	TxToUplink           uint64 `json:"txToUplink"`
	RxToVM               uint64 `json:"rxToVM"`
	Drops                uint64 `json:"drops"`
	LearnedPorts         uint64 `json:"learnedPorts"`
	LearnedIPv4          uint64 `json:"learnedIPv4"`
	LearnedDHCP          uint64 `json:"learnedDHCP"`
	IgnoredWrongBridge   uint64 `json:"ignoredWrongBridge"`
	DHCPOption61Rewrite  uint64 `json:"dhcpOption61Rewrite"`
	DHCPChecksumRewrite  uint64 `json:"dhcpChecksumRewrite"`
	InboundNoVMMatch     uint64 `json:"inboundNoVMMatch"`
	InboundNoLearnedPort uint64 `json:"inboundNoLearnedPort"`
}

func NewController(devicePath string) Controller {
	if devicePath == "" {
		devicePath = DefaultDevicePath
	}
	return Controller{DevicePath: devicePath}
}

func (c Controller) Available() error {
	if _, err := os.Stat(c.DevicePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s device %s is missing; build and load the kernel module with `make macnat-module` and `sudo insmod build/macnat/foxlab_macnat.ko`", ModuleName, c.DevicePath)
		}
		return err
	}
	return nil
}

func (c Controller) Configure(ctx context.Context, sessions []Session) error {
	if len(sessions) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Available(); err != nil {
		return err
	}
	for _, session := range sessions {
		if err := validateSession(session); err != nil {
			return err
		}
	}
	commands, err := configureCommands(sessions)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := c.writeCommand(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

func (c Controller) Clear(ctx context.Context, labID string) error {
	if labID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(c.DevicePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := clearCommand(labID)
	if err != nil {
		return err
	}
	return c.writeCommand(ctx, data)
}

func (c Controller) Status() (state, detail string) {
	report, err := c.readStatus()
	if err != nil {
		return "degraded", err.Error()
	}
	if len(report.Sessions) == 1 {
		session := report.Sessions[0]
		return statusState(report, session), statusDetail(session)
	}
	if len(report.Sessions) > 1 {
		return report.State, fmt.Sprintf("%s; sessions=%d", report.Message, len(report.Sessions))
	}
	return report.State, report.Message
}

func (c Controller) StatusForSwitch(switchID string) (state, detail string) {
	report, err := c.readStatus()
	if err != nil {
		return "degraded", err.Error()
	}
	for _, session := range report.Sessions {
		if session.SwitchID == switchID {
			return statusState(report, session), statusDetail(session)
		}
	}
	if switchID == "" && len(report.Sessions) == 1 {
		session := report.Sessions[0]
		return statusState(report, session), statusDetail(session)
	}
	return "degraded", fmt.Sprintf("macnat session for switch %q is missing; %s", switchID, report.Message)
}

func (c Controller) readStatus() (driverStatus, error) {
	if err := c.Available(); err != nil {
		return driverStatus{}, err
	}
	data, err := os.ReadFile(c.DevicePath)
	if err != nil {
		return driverStatus{}, fmt.Errorf("read %s: %w", c.DevicePath, err)
	}
	var report driverStatus
	if err := json.Unmarshal(data, &report); err != nil {
		return driverStatus{}, fmt.Errorf("parse %s status: %w", c.DevicePath, err)
	}
	if report.State == "" {
		report.State = "degraded"
	}
	if report.Message == "" {
		report.Message = "kernel driver returned no status message"
	}
	if len(report.Sessions) == 0 && (report.SwitchID != "" || report.Uplink != "" || report.Bridge != "") {
		report.Sessions = []driverSessionStatus{{
			State:                report.State,
			Message:              report.Message,
			LabID:                report.LabID,
			SwitchID:             report.SwitchID,
			Bridge:               report.Bridge,
			Uplink:               report.Uplink,
			VMCount:              report.VMCount,
			TxToUplink:           report.TxToUplink,
			RxToVM:               report.RxToVM,
			Drops:                report.Drops,
			LearnedPorts:         report.LearnedPorts,
			LearnedIPv4:          report.LearnedIPv4,
			LearnedDHCP:          report.LearnedDHCP,
			IgnoredWrongBridge:   report.IgnoredWrongBridge,
			DHCPOption61Rewrite:  report.DHCPOption61Rewrite,
			DHCPChecksumRewrite:  report.DHCPChecksumRewrite,
			InboundNoVMMatch:     report.InboundNoVMMatch,
			InboundNoLearnedPort: report.InboundNoLearnedPort,
		}}
	}
	return report, nil
}

func statusState(report driverStatus, session driverSessionStatus) string {
	if session.State != "" {
		return session.State
	}
	return report.State
}

func statusDetail(report driverSessionStatus) string {
	message := report.Message
	if message == "" {
		message = "kernel driver returned no status message"
	}
	return fmt.Sprintf("%s; switch=%s bridge=%s uplink=%s vms=%d tx=%d rx=%d drops=%d learnedPorts=%d learnedIPv4=%d learnedDHCP=%d wrongBridge=%d dhcpOpt61=%d dhcpCsum=%d inboundNoVM=%d inboundNoPort=%d",
		message, report.SwitchID, report.Bridge, report.Uplink,
		report.VMCount, report.TxToUplink, report.RxToVM, report.Drops,
		report.LearnedPorts, report.LearnedIPv4, report.LearnedDHCP,
		report.IgnoredWrongBridge, report.DHCPOption61Rewrite,
		report.DHCPChecksumRewrite, report.InboundNoVMMatch,
		report.InboundNoLearnedPort)
}

func validateSession(session Session) error {
	if session.LabID == "" || session.SwitchID == "" || session.Bridge == "" || session.Uplink == "" {
		return fmt.Errorf("macnat session needs labID, switchID, bridge and uplink")
	}
	if _, err := net.InterfaceByName(session.Bridge); err != nil {
		return fmt.Errorf("macnat bridge %q is not present: %w", session.Bridge, err)
	}
	if _, err := net.InterfaceByName(session.Uplink); err != nil {
		return fmt.Errorf("macnat uplink %q is not present: %w", session.Uplink, err)
	}
	for _, mac := range session.VMMACs {
		if _, err := net.ParseMAC(mac); err != nil {
			return fmt.Errorf("macnat VM MAC %q is invalid: %w", mac, err)
		}
	}
	return nil
}

func configureCommand(session Session) ([]byte, error) {
	values := []string{session.LabID, session.SwitchID, session.Bridge, session.Uplink}
	for _, mac := range session.VMMACs {
		values = append(values, mac)
	}
	for _, value := range values {
		if strings.ContainsAny(value, " \t\r\n") {
			return nil, fmt.Errorf("macnat command value %q contains whitespace", value)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "configure labID=%s switchID=%s bridge=%s uplink=%s", session.LabID, session.SwitchID, session.Bridge, session.Uplink)
	for _, macText := range session.VMMACs {
		mac, err := net.ParseMAC(macText)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, " mac=%s", mac.String())
	}
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

func configureCommands(sessions []Session) ([][]byte, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	labID := sessions[0].LabID
	commands := make([][]byte, 0, len(sessions)+1)
	clear, err := clearCommand(labID)
	if err != nil {
		return nil, err
	}
	commands = append(commands, clear)
	for _, session := range sessions {
		if session.LabID != labID {
			return nil, fmt.Errorf("macnat configure cannot mix lab IDs %q and %q", labID, session.LabID)
		}
		command, err := configureCommand(session)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func clearCommand(labID string) ([]byte, error) {
	if strings.ContainsAny(labID, " \t\r\n") {
		return nil, fmt.Errorf("macnat command value %q contains whitespace", labID)
	}
	return []byte(fmt.Sprintf("clear labID=%s\n", labID)), nil
}

func (c Controller) writeCommand(ctx context.Context, data []byte) error {
	f, err := os.OpenFile(c.DevicePath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("short write to %s: wrote %d of %d bytes", c.DevicePath, n, len(data))
	}
	return nil
}

func NeedsController(mode string) bool {
	return strings.TrimSpace(mode) == Mode
}
