package ambassador

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/patch/agora-os/internal/schema"
)

// SupervisorClientImpl talks to the agent supervisor over its Unix socket.
type SupervisorClientImpl struct {
	socketPath string
}

// NewSupervisorClient creates a supervisor client targeting the given Unix socket.
func NewSupervisorClient(socketPath string) *SupervisorClientImpl {
	return &SupervisorClientImpl{socketPath: socketPath}
}

// EnsureWorker asks the supervisor to create or reuse a worker for the given request.
func (c *SupervisorClientImpl) EnsureWorker(req schema.EnsureWorkerRequest) (*schema.EnsureWorkerResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal ensure worker request: %w", err)
	}

	resp, err := c.call(schema.Request{
		Method: "ensure_worker",
		Body:   body,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("supervisor: %s", string(resp.Body))
	}

	var ensureResp schema.EnsureWorkerResponse
	if err := json.Unmarshal(resp.Body, &ensureResp); err != nil {
		return nil, fmt.Errorf("unmarshal ensure worker response: %w", err)
	}
	return &ensureResp, nil
}

// ReleaseWorker asks the supervisor to release a worker lease.
func (c *SupervisorClientImpl) ReleaseWorker(req schema.ReleaseWorkerRequest) (*schema.ReleaseWorkerResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal release worker request: %w", err)
	}

	resp, err := c.call(schema.Request{
		Method: "release_worker",
		Body:   body,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("supervisor: %s", string(resp.Body))
	}

	var releaseResp schema.ReleaseWorkerResponse
	if err := json.Unmarshal(resp.Body, &releaseResp); err != nil {
		return nil, fmt.Errorf("unmarshal release worker response: %w", err)
	}
	return &releaseResp, nil
}

// ListWorkers returns workers visible to the requester.
func (c *SupervisorClientImpl) ListWorkers(req schema.ListWorkersRequest) (*schema.ListWorkersResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal list workers request: %w", err)
	}

	resp, err := c.call(schema.Request{
		Method: "list_workers",
		Body:   body,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("supervisor: %s", string(resp.Body))
	}

	var listResp schema.ListWorkersResponse
	if err := json.Unmarshal(resp.Body, &listResp); err != nil {
		return nil, fmt.Errorf("unmarshal list workers response: %w", err)
	}
	return &listResp, nil
}

// DescribeProfiles returns the profiles that the requester is allowed to request.
func (c *SupervisorClientImpl) DescribeProfiles() (*schema.DescribeProfilesResponse, error) {
	resp, err := c.call(schema.Request{
		Method: "describe_profiles",
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("supervisor: %s", string(resp.Body))
	}

	var descResp schema.DescribeProfilesResponse
	if err := json.Unmarshal(resp.Body, &descResp); err != nil {
		return nil, fmt.Errorf("unmarshal describe profiles response: %w", err)
	}
	return &descResp, nil
}

// call performs a single request/response round-trip over the supervisor Unix socket.
func (c *SupervisorClientImpl) call(req schema.Request) (schema.Response, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return schema.Response{}, fmt.Errorf("supervisor dial: %w", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return schema.Response{}, fmt.Errorf("send request: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return schema.Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}
