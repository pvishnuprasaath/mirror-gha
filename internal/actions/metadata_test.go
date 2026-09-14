package actions

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestParseMetadata_ActionYml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Hello Action'
description: 'says hello'
inputs:
  who-to-greet:
    description: 'who to greet'
    required: true
    default: 'World'
outputs:
  greeting:
    description: 'the greeting'
runs:
  using: 'node20'
  main: 'index.js'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Name != "Hello Action" {
		t.Errorf("Name = %q, want %q", meta.Name, "Hello Action")
	}
	input, ok := meta.Inputs["who-to-greet"]
	if !ok {
		t.Fatal(`Inputs["who-to-greet"] not found`)
	}
	if input.Default != "World" || !input.Required {
		t.Errorf("input = %+v, want Default=World Required=true", input)
	}
	if meta.Runs.Using != "node20" || meta.Runs.Main != "index.js" {
		t.Errorf("Runs = %+v, want Using=node20 Main=index.js", meta.Runs)
	}
}

func TestParseMetadata_ActionYamlExtension(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yaml", `
name: 'Yaml Extension Action'
runs:
  using: 'node20'
  main: 'index.js'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Name != "Yaml Extension Action" {
		t.Errorf("Name = %q, want %q", meta.Name, "Yaml Extension Action")
	}
}

func TestParseMetadata_DockerRuns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Docker Action'
runs:
  using: 'docker'
  image: 'Dockerfile'
  entrypoint: '/entrypoint.sh'
  args:
    - '--verbose'
    - '--name'
    - 'mirror-gha'
  env:
    GREETING_STYLE: 'formal'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Runs.Using != "docker" || meta.Runs.Image != "Dockerfile" {
		t.Errorf("Runs.Using/Image = %q/%q, want docker/Dockerfile", meta.Runs.Using, meta.Runs.Image)
	}
	if meta.Runs.Entrypoint != "/entrypoint.sh" {
		t.Errorf("Runs.Entrypoint = %q, want /entrypoint.sh", meta.Runs.Entrypoint)
	}
	wantArgs := []string{"--verbose", "--name", "mirror-gha"}
	if len(meta.Runs.Args) != len(wantArgs) {
		t.Fatalf("Runs.Args = %v, want %v", meta.Runs.Args, wantArgs)
	}
	for i, a := range wantArgs {
		if meta.Runs.Args[i] != a {
			t.Errorf("Runs.Args[%d] = %q, want %q", i, meta.Runs.Args[i], a)
		}
	}
	if meta.Runs.Env["GREETING_STYLE"] != "formal" {
		t.Errorf(`Runs.Env["GREETING_STYLE"] = %q, want %q`, meta.Runs.Env["GREETING_STYLE"], "formal")
	}
}

func TestParseMetadata_CompositeRuns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Composite Action'
inputs:
  who-to-greet:
    default: 'World'
outputs:
  greeting:
    description: 'the greeting'
    value: '${{ steps.greet.outputs.greeting }}'
runs:
  using: 'composite'
  steps:
    - id: greet
      run: 'echo "greeting=Hello, ${{ inputs.who-to-greet }}!" >> "$GITHUB_OUTPUT"'
      shell: 'sh'
    - uses: './some/nested-action'
      with:
        key: 'value'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Runs.Using != "composite" {
		t.Errorf("Runs.Using = %q, want composite", meta.Runs.Using)
	}
	if len(meta.Runs.Steps) != 2 {
		t.Fatalf("Runs.Steps = %d entries, want 2", len(meta.Runs.Steps))
	}
	if meta.Runs.Steps[0].ID != "greet" || meta.Runs.Steps[0].Shell != "sh" {
		t.Errorf("Runs.Steps[0] = %+v, want ID=greet Shell=sh", meta.Runs.Steps[0])
	}
	if meta.Runs.Steps[1].Uses != "./some/nested-action" || meta.Runs.Steps[1].With["key"] != "value" {
		t.Errorf("Runs.Steps[1] = %+v, want Uses=./some/nested-action With[key]=value", meta.Runs.Steps[1])
	}
	output, ok := meta.Outputs["greeting"]
	if !ok {
		t.Fatal(`Outputs["greeting"] not found`)
	}
	if output.Value != "${{ steps.greet.outputs.greeting }}" {
		t.Errorf("Outputs[greeting].Value = %q, want %q", output.Value, "${{ steps.greet.outputs.greeting }}")
	}
}

func TestParseMetadata_PostAction(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Post Action'
runs:
  using: 'node20'
  main: 'dist/restore/index.js'
  post: 'dist/save/index.js'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Runs.Post != "dist/save/index.js" {
		t.Errorf("Runs.Post = %q, want %q", meta.Runs.Post, "dist/save/index.js")
	}
}

func TestParseMetadata_MissingFileIsError(t *testing.T) {
	_, err := ParseMetadata(t.TempDir())
	if err == nil {
		t.Fatal("ParseMetadata() error = nil, want error when neither action.yml nor action.yaml exists")
	}
}
