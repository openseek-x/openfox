package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ServiceState string

const (
	StateStarting  ServiceState = "STARTING"
	StateRunning   ServiceState = "RUNNING"
	StateStopping  ServiceState = "STOPPING"
	StateStopped   ServiceState = "STOPPED"
	StateReconcile ServiceState = "RECONCILE_REQUIRED"
)

type Status struct {
	State       ServiceState `json:"state"`
	RemoteState RemoteState  `json:"remote_state,omitempty"`
	PID         int          `json:"pid"`
	StartedAt   time.Time    `json:"started_at"`
	SocketPath  string       `json:"socket_path"`
	CenterEpoch int64        `json:"center_epoch"`
	Reason      string       `json:"reason,omitempty"`
	// ActiveRuns 是本机正在执行（未终态）的 Run 数（桌面端“防止休眠”使用）；未启用执行时为 0。
	ActiveRuns int `json:"active_runs"`
}

type controlRequest struct {
	Method string `json:"method"`
}

type controlResponse struct {
	OK     bool   `json:"ok"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

type ControlServer struct {
	SocketPath string
	Now        func() time.Time
	PID        int
	// ActiveRuns 返回当前活动 Run 数；为空时上报 0。
	ActiveRuns func() int

	mu     sync.RWMutex
	status Status
}

func NewControlServer(socketPath string) *ControlServer {
	return &ControlServer{
		SocketPath: socketPath,
		Now:        time.Now,
		PID:        os.Getpid(),
		status:     Status{State: StateStarting, SocketPath: socketPath},
	}
}

func (s *ControlServer) SetRunning(centerEpoch int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = StateRunning
	s.status.PID = s.PID
	s.status.StartedAt = s.Now().UTC()
	s.status.CenterEpoch = centerEpoch
}

func (s *ControlServer) SetRemoteState(state RemoteState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.RemoteState = state
}

func (s *ControlServer) SetReconcile(reason string) {
	s.SetReconcileAt(0, reason)
}

func (s *ControlServer) SetReconcileAt(centerEpoch int64, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = StateReconcile
	if centerEpoch > 0 {
		s.status.CenterEpoch = centerEpoch
	}
	s.status.Reason = reason
}

func (s *ControlServer) Status() Status {
	s.mu.RLock()
	status, active := s.status, s.ActiveRuns
	s.mu.RUnlock()
	if active != nil {
		status.ActiveRuns = active()
	}
	return status
}

func (s *ControlServer) Serve(ctx context.Context) error {
	if s == nil || s.SocketPath == "" || s.Now == nil {
		return errors.New("daemon control server is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create daemon socket directory: %w", err)
	}
	_ = os.Remove(s.SocketPath)
	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on daemon socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.SocketPath)
	}()
	if err := os.Chmod(s.SocketPath, 0o600); err != nil {
		return fmt.Errorf("protect daemon socket: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept daemon control connection: %w", err)
		}
		go s.handle(connection)
	}
}

func (s *ControlServer) handle(connection net.Conn) {
	defer connection.Close()
	var request controlRequest
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&request); err != nil {
		_ = json.NewEncoder(connection).Encode(controlResponse{Error: "invalid request"})
		return
	}
	if request.Method != "status" {
		_ = json.NewEncoder(connection).Encode(controlResponse{Error: "unsupported method"})
		return
	}
	_ = json.NewEncoder(connection).Encode(controlResponse{OK: true, Status: s.Status()})
}

func QuerySocket(ctx context.Context, socketPath string) (Status, error) {
	if socketPath == "" {
		return Status{}, errors.New("daemon socket path is required")
	}
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Status{}, fmt.Errorf("connect to daemon socket: %w", err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(controlRequest{Method: "status"}); err != nil {
		return Status{}, err
	}
	var response controlResponse
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return Status{}, err
	}
	if !response.OK {
		return Status{}, errors.New(response.Error)
	}
	return response.Status, nil
}
