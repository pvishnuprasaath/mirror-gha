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

// ActionStep is one entry in a composite action's `runs.steps:` list.
// Mirrors engine.Step's shape for the fields composite steps actually
// support — deliberately not engine.Step itself: internal/engine already
// imports internal/actions, so the reverse import would cycle.
// engine.Step's TimeoutMinutes isn't here — composite steps don't
// support it.
type ActionStep struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Uses             string            `yaml:"uses"`
	With             map[string]string `yaml:"with"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
}

// ActionOutput is one entry in an action.yml's `outputs:` map. Value is
// only meaningful for composite actions — a `${{ steps.x.outputs.y }}`
// expression evaluated against that composite's own nested step scope.
// JS/Docker actions' outputs are purely descriptive (the action itself
// writes $GITHUB_OUTPUT directly), which is why this field was absent
// before composite actions needed it.
type ActionOutput struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}

// ActionRuns is an action.yml's `runs:` block.
type ActionRuns struct {
	Using      string            `yaml:"using"`
	Main       string            `yaml:"main"`
	Image      string            `yaml:"image"`
	Entrypoint string            `yaml:"entrypoint"`
	Args       []string          `yaml:"args"`
	Env        map[string]string `yaml:"env"`
	Steps      []ActionStep      `yaml:"steps"`
}

// ActionMetadata is a parsed action.yml/action.yaml.
type ActionMetadata struct {
	Name    string                  `yaml:"name"`
	Inputs  map[string]ActionInput  `yaml:"inputs"`
	Outputs map[string]ActionOutput `yaml:"outputs"`
	Runs    ActionRuns              `yaml:"runs"`
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
