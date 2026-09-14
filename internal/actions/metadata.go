package actions

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ActionInput is one entry in an action.yml's `inputs:` map.
type ActionInput struct {
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
}

// ActionRuns is an action.yml's `runs:` block.
type ActionRuns struct {
	Using string `yaml:"using"`
	Main  string `yaml:"main"`
}

// ActionMetadata is a parsed action.yml/action.yaml.
type ActionMetadata struct {
	Name    string                 `yaml:"name"`
	Inputs  map[string]ActionInput `yaml:"inputs"`
	Outputs map[string]interface{} `yaml:"outputs"`
	Runs    ActionRuns             `yaml:"runs"`
}

// ParseMetadata reads action.yml or action.yaml from dir's root.
func ParseMetadata(dir string) (*ActionMetadata, error) {
	for _, name := range []string{"action.yml", "action.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var meta ActionMetadata
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("neither action.yml nor action.yaml found in %s", dir)
}
