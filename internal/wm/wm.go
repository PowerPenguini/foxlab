package wm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
)

const (
	serviceName           = "foxlab.wm.WindowManager"
	openWindowMethodName  = "/" + serviceName + "/OpenWindow"
	closeWindowMethodName = "/" + serviceName + "/CloseWindow"
)

type OpenWindowRequest struct {
	AppID string `json:"appId"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Icon  Icon   `json:"icon"`
	Host  string `json:"host"`
	Port  string `json:"port"`
	Path  string `json:"path"`
}

type OpenWindowResponse struct {
	Accepted bool `json:"accepted"`
}

type CloseWindowRequest struct {
	AppID string `json:"appId"`
	Host  string `json:"host"`
	Port  string `json:"port"`
	Path  string `json:"path"`
}

type CloseWindowResponse struct {
	Accepted bool `json:"accepted"`
}

type WindowEvent struct {
	Type  string `json:"type"`
	AppID string `json:"appId"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Icon  Icon   `json:"icon"`
	Host  string `json:"host"`
	Port  string `json:"port"`
	Path  string `json:"path"`
}

type Icon struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Manager struct {
	mu          sync.Mutex
	addr        string
	server      *grpc.Server
	listener    net.Listener
	subscribers map[chan WindowEvent]struct{}
}

func NewManager() *Manager {
	return &Manager{subscribers: make(map[chan WindowEvent]struct{})}
}

func (m *Manager) Start() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addr != "" {
		return m.addr, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	server := grpc.NewServer()
	RegisterWindowManagerServer(server, m)
	m.addr = listener.Addr().String()
	m.listener = listener
	m.server = server
	go func() {
		_ = server.Serve(listener)
	}()
	return m.addr, nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	server := m.server
	listener := m.listener
	m.addr = ""
	m.server = nil
	m.listener = nil
	for ch := range m.subscribers {
		close(ch)
		delete(m.subscribers, ch)
	}
	m.mu.Unlock()

	if server != nil {
		server.Stop()
	}
	if listener != nil {
		_ = listener.Close()
	}
}

func (m *Manager) Subscribe(ctx context.Context) <-chan WindowEvent {
	ch := make(chan WindowEvent, 8)
	m.mu.Lock()
	m.subscribers[ch] = struct{}{}
	m.mu.Unlock()

	go func() {
		<-ctx.Done()
		m.mu.Lock()
		if _, ok := m.subscribers[ch]; ok {
			delete(m.subscribers, ch)
			close(ch)
		}
		m.mu.Unlock()
	}()
	return ch
}

func (m *Manager) OpenWindow(ctx context.Context, req *OpenWindowRequest) (*OpenWindowResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("missing window request")
	}
	if req.Host == "" || req.Port == "" {
		return nil, fmt.Errorf("window request requires host and port")
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	if _, err := url.ParseRequestURI(path); err != nil {
		return nil, fmt.Errorf("invalid window path: %w", err)
	}
	m.publish(WindowEvent{
		Type:  "open-window",
		AppID: req.AppID,
		Name:  req.Name,
		Title: req.Title,
		Icon:  req.Icon,
		Host:  req.Host,
		Port:  req.Port,
		Path:  path,
	})
	return &OpenWindowResponse{Accepted: true}, nil
}

func (m *Manager) CloseWindow(ctx context.Context, req *CloseWindowRequest) (*CloseWindowResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("missing window request")
	}
	if req.AppID == "" {
		return nil, fmt.Errorf("window request requires app id")
	}
	if req.Host == "" || req.Port == "" {
		return nil, fmt.Errorf("window request requires host and port")
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	if _, err := url.ParseRequestURI(path); err != nil {
		return nil, fmt.Errorf("invalid window path: %w", err)
	}
	m.publish(WindowEvent{
		Type:  "close-window",
		AppID: req.AppID,
		Host:  req.Host,
		Port:  req.Port,
		Path:  path,
	})
	return &CloseWindowResponse{Accepted: true}, nil
}

func (m *Manager) publish(event WindowEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func OpenWindow(ctx context.Context, wmAddr string, req OpenWindowRequest) error {
	if wmAddr == "" {
		return fmt.Errorf("wm address is required")
	}
	conn, err := grpc.DialContext(
		ctx,
		wmAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	var res OpenWindowResponse
	if err := conn.Invoke(ctx, openWindowMethodName, &req, &res); err != nil {
		return err
	}
	if !res.Accepted {
		return fmt.Errorf("wm rejected window request")
	}
	return nil
}

func CloseWindow(ctx context.Context, wmAddr string, req CloseWindowRequest) error {
	if wmAddr == "" {
		return fmt.Errorf("wm address is required")
	}
	conn, err := grpc.DialContext(
		ctx,
		wmAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	var res CloseWindowResponse
	if err := conn.Invoke(ctx, closeWindowMethodName, &req, &res); err != nil {
		return err
	}
	if !res.Accepted {
		return fmt.Errorf("wm rejected window request")
	}
	return nil
}

type WindowManagerServer interface {
	OpenWindow(context.Context, *OpenWindowRequest) (*OpenWindowResponse, error)
	CloseWindow(context.Context, *CloseWindowRequest) (*CloseWindowResponse, error)
}

func RegisterWindowManagerServer(server *grpc.Server, service WindowManagerServer) {
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: serviceName,
		HandlerType: (*WindowManagerServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "OpenWindow",
				Handler:    openWindowHandler,
			},
			{
				MethodName: "CloseWindow",
				Handler:    closeWindowHandler,
			},
		},
	}, service)
}

func openWindowHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := new(OpenWindowRequest)
	if err := dec(req); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(WindowManagerServer).OpenWindow(ctx, req)
	}
	info := &grpc.UnaryServerInfo{
		Server:     service,
		FullMethod: openWindowMethodName,
	}
	handler := func(ctx context.Context, request any) (any, error) {
		return service.(WindowManagerServer).OpenWindow(ctx, request.(*OpenWindowRequest))
	}
	return interceptor(ctx, req, info, handler)
}

func closeWindowHandler(service any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := new(CloseWindowRequest)
	if err := dec(req); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(WindowManagerServer).CloseWindow(ctx, req)
	}
	info := &grpc.UnaryServerInfo{
		Server:     service,
		FullMethod: closeWindowMethodName,
	}
	handler := func(ctx context.Context, request any) (any, error) {
		return service.(WindowManagerServer).CloseWindow(ctx, request.(*CloseWindowRequest))
	}
	return interceptor(ctx, req, info, handler)
}

type jsonCodec struct{}

func init() {
	encoding.RegisterCodec(jsonCodec{})
}

func (jsonCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (jsonCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func (jsonCodec) Name() string {
	return "json"
}
