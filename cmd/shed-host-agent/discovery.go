package main

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// shedClientConfig is a minimal local mirror of shed's CLI client config
// (~/.shed/config.yaml). Only the server registry is read; the rest
// (default_server, sheds cache, timeouts) is ignored. Defined locally rather
// than imported because shed's config package is internal — matching this
// project's convention of redefining wire types locally (see internal/protocol).
type shedClientConfig struct {
	Servers map[string]shedServerEntry `yaml:"servers"`
}

type shedServerEntry struct {
	Host     string `yaml:"host"`
	HTTPPort int    `yaml:"http_port"`
}

// defaultShedHTTPPort matches shed's default server HTTP port.
const defaultShedHTTPPort = 8080

// LoadDiscoveredServers reads the shed CLI config at path and returns one
// ServerTarget per registered server, sorted by name for deterministic
// ordering. A missing file is not an error — it yields an empty slice so the
// agent can start and pick servers up live once the file appears. Entries with
// an empty host are skipped.
func LoadDiscoveredServers(path string) ([]ServerTarget, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading shed config %s: %w", path, err)
	}

	var cfg shedClientConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing shed config %s: %w", path, err)
	}

	targets := make([]ServerTarget, 0, len(cfg.Servers))
	for name, entry := range cfg.Servers {
		if entry.Host == "" {
			continue
		}
		port := entry.HTTPPort
		if port == 0 {
			port = defaultShedHTTPPort
		}
		targets = append(targets, ServerTarget{
			Name: name,
			URL:  fmt.Sprintf("http://%s:%d", entry.Host, port),
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets, nil
}
