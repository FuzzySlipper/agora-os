// Package isolation implements the request dispatch and authorization layer
// for the agent isolation service.
//
// Transport decoding, peer UID verification, and method routing live here.
// Actual agent lifecycle operations are delegated to the agent.Manager.
package isolation

import (
	"encoding/json"
	"fmt"
	"log"
	"net"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/peercred"
	"github.com/patch/agora-os/internal/schema"
)

type manager interface {
	Spawn(req schema.SpawnAgentRequest) (*schema.AgentInfo, error)
	Terminate(uid uint32) error
	List() []schema.AgentInfo
}

// Service handles isolation-service requests. It wraps an agent.Manager
// and adds transport decoding, authorization, and method dispatch.
type Service struct {
	mgr       manager
	busSocket string
}

// New creates a Service backed by the given Manager.
func New(mgr manager, busSocket string) *Service {
	return &Service{mgr: mgr, busSocket: busSocket}
}

// HandleConn reads a single request from conn, authorizes it against the
// peer's kernel-verified UID, dispatches it, and writes the response.
func (s *Service) HandleConn(conn net.Conn) {
	defer conn.Close()

	peerUID, err := peercred.PeerUID(conn)
	if err != nil {
		writeError(conn, fmt.Sprintf("peer credentials: %v", err))
		return
	}

	var req schema.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeError(conn, fmt.Sprintf("decode: %v", err))
		return
	}

	var resp schema.Response
	switch req.Method {
	case schema.MethodSpawnAgent:
		resp, err = s.handleSpawn(peerUID, req.Body)
	case schema.MethodTerminateAgent:
		resp, err = s.handleTerminate(peerUID, req.Body)
	case schema.MethodListAgents:
		resp, err = s.handleList(peerUID)
	default:
		writeError(conn, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}

	if err != nil {
		writeError(conn, err.Error())
		return
	}
	json.NewEncoder(conn).Encode(resp)
}

func (s *Service) handleSpawn(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	if peerUID != 0 {
		return schema.Response{}, fmt.Errorf("spawn_agent requires root")
	}
	var req schema.SpawnAgentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return schema.Response{}, fmt.Errorf("bad body: %w", err)
	}
	info, err := s.mgr.Spawn(req)
	if err != nil {
		return schema.Response{}, err
	}
	s.publishLifecycle(schema.TopicAgentLifecycleSpawned, schema.AgentLifecycleEvent{Agent: *info})
	return okResponse(schema.SpawnAgentResponse{Agent: *info}), nil
}

func (s *Service) handleTerminate(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	var req schema.TerminateAgentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return schema.Response{}, fmt.Errorf("bad body: %w", err)
	}
	if peerUID != 0 && req.UID != peerUID {
		return schema.Response{}, fmt.Errorf("cannot terminate another agent")
	}
	var terminated schema.AgentInfo
	for _, info := range s.mgr.List() {
		if info.UID == req.UID {
			terminated = info
			break
		}
	}
	if err := s.mgr.Terminate(req.UID); err != nil {
		return schema.Response{}, err
	}
	if terminated.UID == 0 {
		terminated.UID = req.UID
	}
	terminated.Status = schema.StatusStopped
	s.publishLifecycle(schema.TopicAgentLifecycleTerminated, schema.AgentLifecycleEvent{Agent: terminated})
	return okResponse("terminated"), nil
}

func (s *Service) handleList(peerUID uint32) (schema.Response, error) {
	agents := s.mgr.List()
	if peerUID != 0 {
		filtered := make([]schema.AgentInfo, 0)
		for _, a := range agents {
			if a.UID == peerUID {
				filtered = append(filtered, a)
			}
		}
		agents = filtered
	}
	return okResponse(schema.ListAgentsResponse{Agents: agents}), nil
}

func okResponse(body any) schema.Response {
	b, _ := json.Marshal(body)
	return schema.Response{OK: true, Body: b}
}

func writeError(conn net.Conn, msg string) {
	b, _ := json.Marshal(msg)
	resp := schema.Response{OK: false, Body: b}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("write error response: %v", err)
	}
}

func (s *Service) publishLifecycle(topic string, body any) {
	if s.busSocket == "" {
		return
	}
	client, err := bus.Dial(s.busSocket)
	if err != nil {
		log.Printf("publish %s: connect event bus: %v", topic, err)
		return
	}
	defer client.Close()
	if err := client.Publish(topic, body); err != nil {
		log.Printf("publish %s: %v", topic, err)
	}
}
