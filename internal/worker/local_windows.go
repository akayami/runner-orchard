package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const controlPipe = `\\.\pipe\RunnerOrchardSupervisor`

// The right to append to a file is also the right to create a new instance
// of a named pipe. Grant local desktop clients read/write data without it.
const pipeClientAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE&^windows.FILE_APPEND_DATA

type LocalRequest struct {
	Command string   `json:"command"`
	Hours   *float64 `json:"hours,omitempty"`
}

type LocalResponse struct {
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

type localControls struct {
	mu      sync.Mutex
	pause   PauseFile
	status  Status
	changed chan struct{}
}

func newLocalControls(pause PauseFile) (*localControls, net.Listener, error) {
	return listenLocalControls(controlPipe, pause)
}

func listenLocalControls(path string, pause PauseFile) (*localControls, net.Listener, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	descriptor := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)(A;;0x%x;;;IU)", user.User.Sid, pipeClientAccess)
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: descriptor})
	if err != nil {
		return nil, nil, err
	}
	return &localControls{pause: pause, status: Status{State: "starting"}, changed: make(chan struct{}, 1)}, listener, nil
}

func (c *localControls) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		request, err := readLocalRequest(conn)
		response := LocalResponse{Error: "invalid local request"}
		if err == nil {
			response = c.handle(request, time.Now())
		}
		_ = json.NewEncoder(conn).Encode(response)
		_ = conn.Close()
	}
}

func readLocalRequest(reader io.Reader) (LocalRequest, error) {
	var req LocalRequest
	line, err := bufio.NewReaderSize(reader, 1024).ReadSlice('\n')
	if err != nil {
		return req, err
	}
	return req, json.Unmarshal(line, &req)
}

func (c *localControls) handle(request LocalRequest, now time.Time) LocalResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if request.Command != "pause" && request.Hours != nil {
		return LocalResponse{Error: "hours only apply to pause"}
	}
	switch request.Command {
	case "status":
	case "pause":
		var duration *time.Duration
		if request.Hours != nil {
			nanos := *request.Hours * float64(time.Hour)
			if nanos < 1 || nanos >= float64(math.MaxInt64) {
				return LocalResponse{Error: "pause duration is outside the supported range"}
			}
			d := time.Duration(nanos)
			duration = &d
		}
		until, err := c.pause.Pause(now, duration)
		if err != nil {
			return LocalResponse{Error: "cannot save the manual pause"}
		}
		c.status.PauseUntil = &until
		c.status.Eligible = false
		c.status.State = "pause-requested"
		c.wake()
	case "resume":
		if err := c.pause.Resume(); err != nil {
			return LocalResponse{Error: "cannot clear the manual pause"}
		}
		c.status.PauseUntil = nil
		c.status.Eligible = false
		c.status.State = "checking-eligibility"
		c.wake()
	default:
		return LocalResponse{Error: "unknown local control command"}
	}
	return LocalResponse{Status: c.status}
}

func (c *localControls) wake() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *localControls) publish(status Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A new request made during a network call takes priority over a
	// snapshot from before that request. The next step will publish again.
	if len(c.changed) == 0 {
		c.status = status
	}
}

func LocalCommand(ctx context.Context, request LocalRequest) (Status, error) {
	return localCommandAt(ctx, controlPipe, request)
}

func localCommandAt(ctx context.Context, path string, request LocalRequest) (Status, error) {
	conn, err := winio.DialPipeAccess(ctx, path, pipeClientAccess)
	if err != nil {
		return Status{}, fmt.Errorf("Windows supervisor is unavailable: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Status{}, err
	}
	var response LocalResponse
	if err := json.NewDecoder(io.LimitReader(conn, 2048)).Decode(&response); err != nil {
		return Status{}, err
	}
	if response.Error != "" {
		return Status{}, errors.New(response.Error)
	}
	return response.Status, nil
}
