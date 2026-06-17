package compositor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

const defaultATSPIBusAddress = "unix:path=/run/user/1001/at-spi/bus"

func (b *Bridge) A11yTree(req schema.A11yTreeRequest) (schema.A11yTreeResponse, error) {
	if req.SurfaceID == "" {
		return schema.A11yTreeResponse{}, fmt.Errorf("surface_id is required")
	}
	if req.Depth <= 0 {
		req.Depth = 8
	}
	surface, err := b.GetSurface(req.SurfaceID)
	if err != nil {
		return schema.A11yTreeResponse{}, err
	}
	client := newATSPIClient()
	root, err := client.treeForPID(int(surface.Client.PID), req.Depth)
	if err != nil {
		return schema.A11yTreeResponse{}, err
	}
	return schema.A11yTreeResponse{SurfaceID: req.SurfaceID, Backend: "at-spi2", Root: root}, nil
}

func (b *Bridge) A11ySemantic(req schema.A11yTreeRequest) (schema.A11yTreeResponse, error) {
	if req.SurfaceID == "" {
		return schema.A11yTreeResponse{}, fmt.Errorf("surface_id is required")
	}
	surface, err := b.GetSurface(req.SurfaceID)
	if err != nil {
		return schema.A11yTreeResponse{}, err
	}
	resp, err := b.A11yTree(req)
	if err != nil {
		return schema.A11yTreeResponse{}, err
	}
	resp.Backend = "at-spi2-semantic"
	if strings.Contains(strings.ToLower(surface.Surface.AppID), "webview") || strings.Contains(strings.ToLower(surface.Surface.AppID), "webkit") {
		resp.Backend = "webkit-atspi-semantic"
	}
	resp.Root = semanticProjection(resp.Root)
	return resp, nil
}

func semanticProjection(node schema.A11yNode) schema.A11yNode {
	// Keep stable node id/action fields while projecting the transport tree into
	// an ASHA-friendly semantic tree. The source remains AT-SPI2 for v1;
	// WebKitGTK exposes its DOM accessibility semantics through this tree.
	sourceRole := node.Role
	semanticRole := semanticRoleForATSPI(sourceRole, node.Interfaces)
	node.BusName = ""
	node.Path = ""
	node.Interfaces = nil
	node.ChildCount = 0
	node.SourceRole = sourceRole
	node.SemanticRole = semanticRole
	node.Role = semanticRole
	children := make([]schema.A11yNode, 0, len(node.Children))
	for _, child := range node.Children {
		projected := semanticProjection(child)
		if isAnonymousSemanticContainer(projected) {
			children = append(children, projected.Children...)
			continue
		}
		children = append(children, projected)
	}
	node.Children = children
	return node
}

func semanticRoleForATSPI(role string, interfaces []string) string {
	lower := strings.ToLower(strings.TrimSpace(role))
	switch {
	case strings.Contains(lower, "push button") || lower == "button":
		return "button"
	case strings.Contains(lower, "text") || strings.Contains(lower, "entry"):
		return "text"
	case strings.Contains(lower, "check"):
		return "checkbox"
	case strings.Contains(lower, "radio"):
		return "radio"
	case strings.Contains(lower, "combo"):
		return "combobox"
	case strings.Contains(lower, "menu"):
		return "menu"
	case strings.Contains(lower, "table"):
		return "table"
	case strings.Contains(lower, "list"):
		return "list"
	case strings.Contains(lower, "link"):
		return "link"
	case strings.Contains(lower, "image") || strings.Contains(lower, "icon"):
		return "image"
	case strings.Contains(lower, "window") || strings.Contains(lower, "frame") || strings.Contains(lower, "dialog"):
		return "window"
	case strings.Contains(lower, "application"):
		return "application"
	case hasA11yInterface(interfaces, "org.a11y.atspi.Action"):
		return "actionable"
	case lower == "" || strings.Contains(lower, "panel") || strings.Contains(lower, "filler") || strings.Contains(lower, "viewport"):
		return "container"
	default:
		return strings.ReplaceAll(lower, " ", "_")
	}
}

func isAnonymousSemanticContainer(node schema.A11yNode) bool {
	return node.SemanticRole == "container" && node.Name == "" && node.Description == "" && len(node.Actions) == 0
}

func hasA11yInterface(interfaces []string, want string) bool {
	short := strings.TrimPrefix(want, "org.a11y.atspi.")
	for _, iface := range interfaces {
		if iface == want || strings.EqualFold(iface, want) || strings.EqualFold(iface, short) {
			return true
		}
	}
	return false
}

func (b *Bridge) A11yFind(req schema.A11yFindRequest) (schema.A11yFindResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return schema.A11yFindResponse{}, fmt.Errorf("name is required")
	}
	depth := req.Depth
	if depth <= 0 {
		depth = 10
	}
	tree, err := b.A11yTree(schema.A11yTreeRequest{SurfaceID: req.SurfaceID, Depth: depth})
	if err != nil {
		return schema.A11yFindResponse{}, err
	}
	needle := strings.ToLower(req.Name)
	var matches []schema.A11yNode
	var walk func(schema.A11yNode)
	walk = func(node schema.A11yNode) {
		if strings.Contains(strings.ToLower(node.Name), needle) || strings.Contains(strings.ToLower(node.Role), needle) || strings.Contains(strings.ToLower(node.Description), needle) {
			copy := node
			copy.Children = nil
			matches = append(matches, copy)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(tree.Root)
	return schema.A11yFindResponse{SurfaceID: req.SurfaceID, Backend: tree.Backend, Matches: matches}, nil
}

func (b *Bridge) A11yClick(peerUID uint32, req schema.A11yClickRequest) (schema.A11yClickResponse, error) {
	if req.NodeID == "" {
		return schema.A11yClickResponse{}, fmt.Errorf("node_id is required")
	}
	if req.ActionIndex < 0 {
		return schema.A11yClickResponse{}, fmt.Errorf("action_index must be non-negative")
	}
	if _, err := b.surfaceForA11yNode(peerUID, req.NodeID); err != nil {
		return schema.A11yClickResponse{}, err
	}
	client := newATSPIClient()
	action := req.ActionIndex
	ok, actionName, err := client.doAction(req.NodeID, action)
	if err != nil {
		return schema.A11yClickResponse{}, err
	}
	if !ok {
		return schema.A11yClickResponse{}, compositorError(schema.ErrorSemanticTreeUnavailable, "AT-SPI action %d on %s returned false", action, req.NodeID)
	}
	return schema.A11yClickResponse{NodeID: req.NodeID, ActionIndex: action, ActionName: actionName, OK: ok}, nil
}

func (b *Bridge) surfaceForA11yNode(peerUID uint32, nodeID string) (string, error) {
	pid, _, _, err := decodeA11yNodeID(nodeID)
	if err != nil {
		return "", err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	var matches []string
	for surfaceID, surface := range b.surfaces {
		if int(surface.Client.PID) == pid {
			matches = append(matches, surfaceID)
		}
	}
	if len(matches) == 0 {
		return "", compositorError(schema.ErrorSurfaceNotFound, "node %s does not belong to a tracked surface", nodeID)
	}
	sort.Strings(matches)
	for _, surfaceID := range matches {
		allowed, _ := b.checkSurfaceAccessLocked(surfaceID, peerUID, schema.AccessPointer)
		if allowed {
			return surfaceID, nil
		}
	}
	return "", compositorError(schema.ErrorInputDenied, "node %s belongs to tracked surface(s) %s but uid %d lacks pointer/action access", nodeID, strings.Join(matches, ","), peerUID)
}

type atspiClient struct {
	address string
}

func newATSPIClient() atspiClient {
	address := os.Getenv("AT_SPI_BUS_ADDRESS")
	if address == "" && fileExists("/run/user/1001/at-spi/bus") {
		address = defaultATSPIBusAddress
	}
	return atspiClient{address: address}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type busNameRecord struct {
	Name    string `json:"name"`
	PID     int    `json:"pid"`
	Process string `json:"process"`
}

func (c atspiClient) treeForPID(pid int, depth int) (schema.A11yNode, error) {
	if c.address == "" {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "AT-SPI2 bus address is unavailable")
	}
	bus, err := c.busNameForPID(pid)
	if err != nil {
		return schema.A11yNode{}, err
	}
	return c.readNode(pid, bus, "/org/a11y/atspi/accessible/root", depth)
}

func (c atspiClient) busNameForPID(pid int) (string, error) {
	out, err := c.busctl("list")
	if err != nil {
		return "", compositorError(schema.ErrorSemanticTreeUnavailable, "list AT-SPI2 bus names: %v", err)
	}
	var records []busNameRecord
	if err := json.Unmarshal(out, &records); err != nil {
		return "", compositorError(schema.ErrorSemanticTreeUnavailable, "parse AT-SPI2 bus names: %v", err)
	}
	for _, rec := range records {
		if rec.PID == pid && strings.HasPrefix(rec.Name, ":") {
			return rec.Name, nil
		}
	}
	return "", compositorError(schema.ErrorSemanticTreeUnavailable, "surface client pid %d is not registered on AT-SPI2 bus", pid)
}

func (c atspiClient) readNode(pid int, bus, path string, depth int) (schema.A11yNode, error) {
	node := schema.A11yNode{ID: encodeA11yNodeID(pid, bus, path), BusName: bus, Path: path}
	var err error
	if node.Name, err = c.stringProperty(bus, path, "Name"); err != nil {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI name for %s: %v", node.ID, err)
	}
	if node.Description, err = c.stringProperty(bus, path, "Description"); err != nil {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI description for %s: %v", node.ID, err)
	}
	if node.Role, err = c.stringMethod(bus, path, "org.a11y.atspi.Accessible", "GetRoleName"); err != nil {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI role for %s: %v", node.ID, err)
	}
	if node.ChildCount, err = c.intProperty(bus, path, "ChildCount"); err != nil {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI child count for %s: %v", node.ID, err)
	}
	if node.Interfaces, err = c.stringSliceMethod(bus, path, "org.a11y.atspi.Accessible", "GetInterfaces"); err != nil {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI interfaces for %s: %v", node.ID, err)
	}
	if hasA11yInterface(node.Interfaces, "org.a11y.atspi.Action") {
		if node.Actions, err = c.actionNames(bus, path); err != nil {
			return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI actions for %s: %v", node.ID, err)
		}
	}
	if depth <= 0 {
		return node, nil
	}
	children, err := c.children(bus, path)
	if err != nil {
		return schema.A11yNode{}, compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI children for %s: %v", node.ID, err)
	}
	for _, child := range children {
		childNode, err := c.readNode(pid, child.Bus, child.Path, depth-1)
		if err != nil {
			return schema.A11yNode{}, err
		}
		node.Children = append(node.Children, childNode)
	}
	return node, nil
}

type objectRef struct{ Bus, Path string }

func (c atspiClient) children(bus, path string) ([]objectRef, error) {
	out, err := c.busctl("call", bus, path, "org.a11y.atspi.Accessible", "GetChildren")
	if err != nil {
		return nil, err
	}
	var msg struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return nil, err
	}
	var data [][][]string
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		return nil, fmt.Errorf("unexpected GetChildren payload: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	refs := make([]objectRef, 0, len(data[0]))
	for _, item := range data[0] {
		if len(item) != 2 || item[0] == "" || item[1] == "" {
			return nil, fmt.Errorf("unexpected GetChildren object ref %#v", item)
		}
		refs = append(refs, objectRef{Bus: item[0], Path: item[1]})
	}
	return refs, nil
}

func (c atspiClient) stringProperty(bus, path, prop string) (string, error) {
	out, err := c.busctl("get-property", bus, path, "org.a11y.atspi.Accessible", prop)
	if err != nil {
		return "", err
	}
	var msg struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return "", err
	}
	return msg.Data, nil
}

func (c atspiClient) intProperty(bus, path, prop string) (int, error) {
	out, err := c.busctl("get-property", bus, path, "org.a11y.atspi.Accessible", prop)
	if err != nil {
		return 0, err
	}
	var msg struct {
		Data int `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return 0, err
	}
	return msg.Data, nil
}

func (c atspiClient) stringMethod(bus, path, iface, method string, args ...string) (string, error) {
	callArgs := []string{"call", bus, path, iface, method}
	callArgs = append(callArgs, args...)
	out, err := c.busctl(callArgs...)
	if err != nil {
		return "", err
	}
	var msg struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return "", err
	}
	var list []string
	if err := json.Unmarshal(msg.Data, &list); err == nil && len(list) > 0 {
		return list[0], nil
	}
	var value string
	if err := json.Unmarshal(msg.Data, &value); err == nil {
		return value, nil
	}
	return "", fmt.Errorf("unexpected string payload for %s.%s", iface, method)
}

func (c atspiClient) stringSliceMethod(bus, path, iface, method string) ([]string, error) {
	out, err := c.busctl("call", bus, path, iface, method)
	if err != nil {
		return nil, err
	}
	var msg struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return nil, err
	}
	var outer [][]string
	if err := json.Unmarshal(msg.Data, &outer); err == nil && len(outer) > 0 {
		return outer[0], nil
	}
	var flat []string
	if err := json.Unmarshal(msg.Data, &flat); err == nil {
		return flat, nil
	}
	return nil, fmt.Errorf("unexpected string slice payload for %s.%s", iface, method)
}

func (c atspiClient) actionNames(bus, path string) ([]string, error) {
	out, err := c.busctl("get-property", bus, path, "org.a11y.atspi.Action", "NActions")
	if err != nil {
		return nil, err
	}
	var msg struct {
		Data int `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return nil, err
	}
	if msg.Data <= 0 {
		return nil, nil
	}
	actions := make([]string, 0, msg.Data)
	for i := 0; i < msg.Data; i++ {
		name, err := c.stringMethod(bus, path, "org.a11y.atspi.Action", "GetName", "i", strconv.Itoa(i))
		if err != nil {
			return nil, err
		}
		actions = append(actions, name)
	}
	return actions, nil
}

func (c atspiClient) doAction(nodeID string, index int) (bool, string, error) {
	pid, bus, path, err := decodeA11yNodeID(nodeID)
	if err != nil {
		return false, "", err
	}
	currentBus, err := c.busNameForPID(pid)
	if err != nil {
		return false, "", err
	}
	if currentBus != bus {
		return false, "", compositorError(schema.ErrorSemanticTreeUnavailable, "node %s no longer belongs to pid %d", nodeID, pid)
	}
	name, err := c.stringMethod(bus, path, "org.a11y.atspi.Action", "GetName", "i", strconv.Itoa(index))
	if err != nil {
		return false, "", compositorError(schema.ErrorSemanticTreeUnavailable, "read AT-SPI action name on %s: %v", nodeID, err)
	}
	out, err := c.busctl("call", bus, path, "org.a11y.atspi.Action", "DoAction", "i", strconv.Itoa(index))
	if err != nil {
		return false, name, compositorError(schema.ErrorSemanticTreeUnavailable, "invoke AT-SPI action on %s: %v", nodeID, err)
	}
	var msg struct {
		Data []bool `json:"data"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		return false, name, err
	}
	ok := len(msg.Data) > 0 && msg.Data[0]
	return ok, name, nil
}

func (c atspiClient) busctl(args ...string) ([]byte, error) {
	if c.address == "" {
		return nil, fmt.Errorf("AT-SPI2 bus address unavailable")
	}
	cmdArgs := append([]string{"--address", c.address, "--json=short"}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "busctl", cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("busctl timed out")
	}
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return bytes.TrimSpace(out), nil
}

func encodeA11yNodeID(pid int, bus, path string) string {
	return strconv.Itoa(pid) + "|" + bus + "|" + path
}

func decodeA11yNodeID(id string) (int, string, string, error) {
	parts := strings.SplitN(id, "|", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return 0, "", "", fmt.Errorf("node id must be '<pid>|<bus>|<object-path>'")
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		return 0, "", "", fmt.Errorf("node id has invalid pid")
	}
	return pid, parts[1], parts[2], nil
}

func withA11yLaunchEnv(env []string, reqEnv map[string]string) []string {
	if _, ok := reqEnv["DBUS_SESSION_BUS_ADDRESS"]; !ok && fileExists("/run/user/1001/bus") {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus")
	}
	if _, ok := reqEnv["AT_SPI_BUS_ADDRESS"]; !ok && fileExists("/run/user/1001/at-spi/bus") {
		env = append(env, "AT_SPI_BUS_ADDRESS="+defaultATSPIBusAddress)
	}
	if _, ok := reqEnv["NO_AT_BRIDGE"]; !ok {
		env = append(env, "NO_AT_BRIDGE=0")
	}
	return env
}
