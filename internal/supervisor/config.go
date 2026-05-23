package supervisor

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/patch/agora-os/internal/schema"
)

// Config is the on-disk agent-supervisor configuration contract. It defines
// the available worker profiles and the kernel-UID grants allowed to request
// them. Main wires this into immutable registries at startup.
type Config struct {
	Profiles []schema.WorkerProfile `json:"profiles"`
	Grants   []schema.ProfileGrant  `json:"grants"`
}

// DefaultConfig provides a safe built-in fallback for development and for hosts
// that have not installed /etc/agent-os/agent-supervisor.json yet.
func DefaultConfig() Config {
	return Config{
		Profiles: []schema.WorkerProfile{
			{
				Profile:         "repo-inspector",
				Runtime:         schema.RuntimeLocalLLM,
				Tools:           []string{"fs.read", "git.diff", "ripgrep"},
				CPUQuota:        "50%",
				MemoryMax:       "2G",
				NetAccess:       schema.NetDeny,
				WatchPaths:      []string{"/repo"},
				MaxLeaseSeconds: 900,
				ReusePolicy:     schema.ReuseSession,
			},
			{
				Profile:         "patch-writer",
				Runtime:         schema.RuntimeDeterministic,
				Tools:           []string{"fs.write", "git.commit", "patch"},
				CPUQuota:        "100%",
				MemoryMax:       "4G",
				NetAccess:       schema.NetLocalOnly,
				MaxLeaseSeconds: 1800,
				ReusePolicy:     schema.ReuseSession,
			},
			{
				Profile:         "ui-observer",
				Runtime:         schema.RuntimeLocalLLM,
				Tools:           []string{"screenshot", "dom.query", "ui.read"},
				CPUQuota:        "50%",
				MemoryMax:       "4G",
				NetAccess:       schema.NetAllow,
				MaxLeaseSeconds: 600,
				ReusePolicy:     schema.ReuseLease,
			},
		},
		Grants: []schema.ProfileGrant{
			{
				RequesterUID:         0,
				AllowedProfiles:      []string{"repo-inspector", "patch-writer", "ui-observer"},
				MaxConcurrentWorkers: 5,
				MaxLeaseSeconds:      3600,
			},
		},
	}
}

// LoadConfig reads and validates an agent-supervisor JSON config file.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("config path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse supervisor config %q: %w", path, err)
	}
	if len(cfg.Profiles) == 0 {
		return Config{}, fmt.Errorf("supervisor config %q has no profiles", path)
	}
	if len(cfg.Grants) == 0 {
		return Config{}, fmt.Errorf("supervisor config %q has no grants", path)
	}
	return cfg, nil
}
