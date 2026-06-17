package compositor

import (
	"os"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

func TestSemanticProjectionNormalizesRolesAndFlattensAnonymousContainers(t *testing.T) {
	raw := schema.A11yNode{
		ID:         "1|:1.2|/root",
		Role:       "application",
		BusName:    ":1.2",
		Path:       "/root",
		ChildCount: 1,
		Interfaces: []string{"org.a11y.atspi.Accessible"},
		Children: []schema.A11yNode{{
			ID:         "1|:1.2|/panel",
			Role:       "panel",
			Interfaces: []string{"org.a11y.atspi.Accessible"},
			Children: []schema.A11yNode{{
				ID:         "1|:1.2|/button",
				Name:       "OK",
				Role:       "push button",
				BusName:    ":1.2",
				Path:       "/button",
				Interfaces: []string{"org.a11y.atspi.Accessible", "org.a11y.atspi.Action"},
				Actions:    []string{"click"},
			}},
		}},
	}

	projected := semanticProjection(raw)
	if projected.Role != "application" || projected.SemanticRole != "application" || projected.SourceRole != "application" {
		t.Fatalf("root roles = role:%q semantic:%q source:%q", projected.Role, projected.SemanticRole, projected.SourceRole)
	}
	if projected.BusName != "" || projected.Path != "" || len(projected.Interfaces) != 0 || projected.ChildCount != 0 {
		t.Fatalf("transport details leaked into semantic projection: %+v", projected)
	}
	if len(projected.Children) != 1 {
		t.Fatalf("expected anonymous panel to be flattened to one child, got %+v", projected.Children)
	}
	button := projected.Children[0]
	if button.Name != "OK" || button.Role != "button" || button.SemanticRole != "button" || button.SourceRole != "push button" {
		t.Fatalf("button projection = %+v", button)
	}
	if button.BusName != "" || button.Path != "" || len(button.Interfaces) != 0 {
		t.Fatalf("button transport details leaked: %+v", button)
	}
}

func TestSurfaceForA11yNodeRequiresTrackedSurfaceAndAccess(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.mu.Lock()
	bridge.surfaces["view-owner"] = schema.CompositorTrackedSurface{
		Surface:   schema.CompositorSurface{ID: "view-owner"},
		Client:    schema.CompositorClientIdentity{PID: 4242, UID: 1001, GID: 1001},
		UpdatedAt: time.Now(),
	}
	bridge.mu.Unlock()

	nodeID := encodeA11yNodeID(4242, ":1.42", "/org/a11y/atspi/accessible/7")
	if surfaceID, err := bridge.surfaceForA11yNode(1001, nodeID); err != nil || surfaceID != "view-owner" {
		t.Fatalf("owner surfaceForA11yNode = %q, %v", surfaceID, err)
	}
	if _, err := bridge.surfaceForA11yNode(2002, nodeID); err == nil {
		t.Fatal("expected non-owner without grant to be denied")
	} else if class, _ := classifyError(err); class != schema.ErrorInputDenied {
		t.Fatalf("deny class = %q, want %q (%v)", class, schema.ErrorInputDenied, err)
	}
	missingNode := encodeA11yNodeID(9999, ":1.99", "/org/a11y/atspi/accessible/root")
	if _, err := bridge.surfaceForA11yNode(1001, missingNode); err == nil {
		t.Fatal("expected untracked pid to be rejected")
	} else if class, _ := classifyError(err); class != schema.ErrorSurfaceNotFound {
		t.Fatalf("missing class = %q, want %q (%v)", class, schema.ErrorSurfaceNotFound, err)
	}
}
