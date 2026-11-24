package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"gopkg.in/yaml.v3"
)

type rustTunGroup struct {
	Name    string         `yaml:"name"`
	Members []string       `yaml:"proxies"`
	Use     []string       `yaml:"use,omitempty"`
	Fields  map[string]any `yaml:",inline"`
}

// Expand each referenced file once, retaining the order of use and of the provider rows.
func materializeRustTun(home string, body []byte, choice string) ([]byte, error) {
	var config struct {
		Providers map[string]struct{ Type, Path string } `yaml:"proxy-providers,omitempty"`
		Proxies   []map[string]any                       `yaml:"proxies"`
		Groups    []rustTunGroup                         `yaml:"proxy-groups"`
		Fields    map[string]any                         `yaml:",inline"`
	}
	if err := yaml.Unmarshal(body, &config); err != nil {
		return nil, err
	}
	names := make(map[string][]string)
	seen := make(map[string]map[string]any)
	for _, row := range config.Proxies {
		seen[stringValue(row["name"])] = row
	}
	for i := range config.Groups {
		group := &config.Groups[i]
		for _, ref := range group.Use {
			members, loaded := names[ref]
			if !loaded {
				provider, ok := config.Providers[ref]
				if !ok || provider.Type != "file" || !filepath.IsLocal(provider.Path) {
					return nil, fmt.Errorf("invalid Rust TUN file provider %q in %q", ref, group.Name)
				}
				data, err := os.ReadFile(filepath.Join(home, provider.Path))
				if err != nil {
					return nil, err
				}
				var file struct {
					Proxies []map[string]any `yaml:"proxies"`
				}
				if err := yaml.Unmarshal(data, &file); err != nil {
					return nil, fmt.Errorf("provider %q: %w", ref, err)
				}
				for _, row := range file.Proxies {
					name, ok := row["name"].(string)
					if !ok || name == "" {
						continue
					}
					if existing, found := seen[name]; found {
						if !reflect.DeepEqual(existing, row) {
							return nil, fmt.Errorf("ambiguous provider node %q", name)
						}
					} else {
						config.Proxies = append(config.Proxies, row)
						seen[name] = row
					}
					members = append(members, name)
				}
				names[ref] = members
			}
			group.Members = append(group.Members, members...)
		}
		group.Use = nil
		if group.Name == appName && choice != "" {
			idx := slices.Index(group.Members, choice)
			if idx < 0 {
				return nil, fmt.Errorf("selected outbound %q missing from %s", choice, group.Name)
			}
			copy(group.Members[1:idx+1], group.Members[:idx])
			group.Members[0] = choice
		}
	}
	config.Providers = nil
	return yaml.Marshal(config)
}
