package engine

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StringOrSlice decodes a YAML field that GitHub Actions allows as either
// a single scalar string or a list of strings (`needs:` is the one used
// in this codebase, but the shape is common across the workflow schema).
type StringOrSlice []string

func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		*s = []string{single}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	default:
		return fmt.Errorf("expected a string or a list of strings")
	}
}

type Step struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
	TimeoutMinutes   float64           `yaml:"timeout-minutes"`
}

// Strategy is a job's `strategy:` block. Matrix.include/exclude are
// parsed but explicitly unsupported (ExpandMatrix returns a clear error
// rather than approximating GitHub Actions' fiddly merge semantics for
// them) — plain axis matrices work today.
type Strategy struct {
	Matrix      map[string]interface{} `yaml:"matrix"`
	FailFast    *bool                  `yaml:"fail-fast"`
	MaxParallel int                    `yaml:"max-parallel"`
}

// RunDefaults is the `run:` block inside a `defaults:` section.
type RunDefaults struct {
	Shell            string `yaml:"shell"`
	WorkingDirectory string `yaml:"working-directory"`
}

// Defaults is a workflow- or job-level `defaults:` block.
type Defaults struct {
	Run RunDefaults `yaml:"run"`
}

type Job struct {
	Name           string            `yaml:"name"`
	RunsOn         string            `yaml:"runs-on"`
	Env            map[string]string `yaml:"env"`
	Steps          []Step            `yaml:"steps"`
	Needs          StringOrSlice     `yaml:"needs"`
	Strategy       *Strategy         `yaml:"strategy"`
	Outputs        map[string]string `yaml:"outputs"`
	TimeoutMinutes float64           `yaml:"timeout-minutes"`
	Defaults       *Defaults         `yaml:"defaults"`
}

type Workflow struct {
	Name     string            `yaml:"name"`
	On       interface{}       `yaml:"on"`
	Env      map[string]string `yaml:"env"`
	Defaults *Defaults         `yaml:"defaults"`
	Jobs     map[string]Job    `yaml:"jobs"`
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
