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
	Uses             string            `yaml:"uses"`
	With             map[string]string `yaml:"with"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
	TimeoutMinutes   float64           `yaml:"timeout-minutes"`
}

// Strategy is a job's `strategy:` block. Matrix.include/exclude are
// parsed as ordinary matrix keys and given real GitHub Actions merge
// semantics by ExpandMatrix.
type Strategy struct {
	Matrix      map[string]interface{} `yaml:"matrix"`
	FailFast    *bool                  `yaml:"fail-fast"`
	MaxParallel int                    `yaml:"max-parallel"`
}

// ContainerSpec is a job's `container:` field, or one entry of its
// `services:` map — GitHub Actions reuses the same shape for both.
type ContainerSpec struct {
	Image       string            `yaml:"image"`
	Env         map[string]string `yaml:"env"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Options     string            `yaml:"options"`
	Credentials map[string]string `yaml:"credentials"`
}

// EnvironmentSpec is a job's `environment:` field. mirror-gha exposes only
// the name (as github.environment) — no approval gate and no
// environment-scoped secrets/vars store exist locally, matching this
// project's documented v1 scope for this field.
type EnvironmentSpec struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// PermissionsSpec is a workflow's or job's `permissions:` field — either
// a bare "read-all"/"write-all" string (All) or a mapping of scope name
// to "read"/"write"/"none" (Scopes). mirror-gha parses and validates the
// shape but doesn't enforce it: there's no real GitHub API surface being
// called locally that permissions could actually restrict, matching this
// project's documented "shim-enforced" v1 scope for this field.
type PermissionsSpec struct {
	All    string
	Scopes map[string]string
}

func decodePermissions(node yaml.Node) (*PermissionsSpec, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		var all string
		if err := node.Decode(&all); err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
		return &PermissionsSpec{All: all}, nil
	case yaml.MappingNode:
		scopes := map[string]string{}
		if err := node.Decode(&scopes); err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
		return &PermissionsSpec{Scopes: scopes}, nil
	default:
		return nil, fmt.Errorf("permissions: must be a string or a mapping")
	}
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
	Name           string                   `yaml:"name"`
	RunsOn         string                   `yaml:"runs-on"`
	Env            map[string]string        `yaml:"env"`
	Steps          []Step                   `yaml:"steps"`
	Needs          StringOrSlice            `yaml:"needs"`
	Strategy       *Strategy                `yaml:"strategy"`
	Outputs        map[string]string        `yaml:"outputs"`
	TimeoutMinutes float64                  `yaml:"timeout-minutes"`
	Defaults       *Defaults                `yaml:"defaults"`
	RawContainer   yaml.Node                `yaml:"container"`
	Services       map[string]ContainerSpec `yaml:"services"`
	RawEnvironment yaml.Node                `yaml:"environment"`
	RawPermissions yaml.Node                `yaml:"permissions"`
}

// Permissions resolves the job's `permissions:` field. Returns nil, nil
// when the job has no permissions: field at all.
func (j *Job) Permissions() (*PermissionsSpec, error) {
	return decodePermissions(j.RawPermissions)
}

// Environment resolves the job's `environment:` field, which GitHub
// Actions allows as either a bare environment-name string or a mapping
// with name/url. Returns nil, nil when the job has no environment: field
// at all.
func (j *Job) Environment() (*EnvironmentSpec, error) {
	switch j.RawEnvironment.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		var name string
		if err := j.RawEnvironment.Decode(&name); err != nil {
			return nil, fmt.Errorf("environment: %w", err)
		}
		return &EnvironmentSpec{Name: name}, nil
	case yaml.MappingNode:
		spec := &EnvironmentSpec{}
		if err := j.RawEnvironment.Decode(spec); err != nil {
			return nil, fmt.Errorf("environment: %w", err)
		}
		return spec, nil
	default:
		return nil, fmt.Errorf("environment: must be a string or a mapping")
	}
}

// Container resolves the job's `container:` field, which GitHub Actions
// allows as either a bare image string or a mapping. Returns nil, nil
// when the job has no container: field at all.
func (j *Job) Container() (*ContainerSpec, error) {
	switch j.RawContainer.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		var image string
		if err := j.RawContainer.Decode(&image); err != nil {
			return nil, fmt.Errorf("container: %w", err)
		}
		return &ContainerSpec{Image: image}, nil
	case yaml.MappingNode:
		spec := &ContainerSpec{}
		if err := j.RawContainer.Decode(spec); err != nil {
			return nil, fmt.Errorf("container: %w", err)
		}
		return spec, nil
	default:
		return nil, fmt.Errorf("container: must be a string or a mapping")
	}
}

type Workflow struct {
	Name           string            `yaml:"name"`
	On             interface{}       `yaml:"on"`
	Env            map[string]string `yaml:"env"`
	Defaults       *Defaults         `yaml:"defaults"`
	Jobs           map[string]Job    `yaml:"jobs"`
	RawPermissions yaml.Node         `yaml:"permissions"`
}

// Permissions resolves the workflow's top-level `permissions:` field.
// Returns nil, nil when the workflow has no permissions: field at all.
func (wf *Workflow) Permissions() (*PermissionsSpec, error) {
	return decodePermissions(wf.RawPermissions)
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
