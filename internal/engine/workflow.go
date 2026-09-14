package engine

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type Step struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
}

type Job struct {
	Name   string            `yaml:"name"`
	RunsOn string            `yaml:"runs-on"`
	Env    map[string]string `yaml:"env"`
	Steps  []Step            `yaml:"steps"`
}

type Workflow struct {
	Name string            `yaml:"name"`
	On   interface{}       `yaml:"on"`
	Env  map[string]string `yaml:"env"`
	Jobs map[string]Job    `yaml:"jobs"`
}

// Parse parses raw GitHub Actions workflow YAML into a Workflow.
func Parse(data []byte) (*Workflow, error) {
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if len(wf.Jobs) == 0 {
		return nil, fmt.Errorf("workflow has no jobs")
	}
	return &wf, nil
}
