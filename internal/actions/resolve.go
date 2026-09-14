package actions

import (
	"fmt"
	"strings"
)

// ActionRef is a parsed `uses:` reference — either a local, workspace-relative
// path, or a Marketplace action identified by owner/repo (and an optional
// subpath, for actions that don't live at their repo's root) plus a ref
// (tag, branch, or commit SHA).
type ActionRef struct {
	Local     bool
	LocalPath string

	// Docker is set for a raw `docker://image:tag` reference — no
	// action.yml, no repo to fetch, no source directory. DockerImage is
	// the image reference with the docker:// prefix stripped.
	Docker      bool
	DockerImage string

	Owner   string
	Repo    string
	Subpath string
	Ref     string
}

// ResolveUsesRef parses a step's `uses:` value.
func ResolveUsesRef(uses string) (ActionRef, error) {
	if strings.HasPrefix(uses, "docker://") {
		return ActionRef{Docker: true, DockerImage: strings.TrimPrefix(uses, "docker://")}, nil
	}
	if strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "../") {
		return ActionRef{Local: true, LocalPath: uses}, nil
	}

	atIdx := strings.LastIndex(uses, "@")
	if atIdx == -1 {
		return ActionRef{}, fmt.Errorf("uses %q is missing a @ref (e.g. owner/repo@v1)", uses)
	}
	repoPart, ref := uses[:atIdx], uses[atIdx+1:]
	if ref == "" {
		return ActionRef{}, fmt.Errorf("uses %q has an empty @ref", uses)
	}

	parts := strings.Split(repoPart, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ActionRef{}, fmt.Errorf("uses %q must be in the form owner/repo[/subpath]@ref", uses)
	}

	ref2 := ActionRef{Owner: parts[0], Repo: parts[1], Ref: ref}
	if len(parts) > 2 {
		ref2.Subpath = strings.Join(parts[2:], "/")
	}
	return ref2, nil
}
