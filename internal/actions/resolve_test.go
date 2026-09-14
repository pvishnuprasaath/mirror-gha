package actions

import "testing"

func TestResolveUsesRef_LocalPath(t *testing.T) {
	ref, err := ResolveUsesRef("./local/action")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if !ref.Local {
		t.Error("Local = false, want true")
	}
	if ref.LocalPath != "./local/action" {
		t.Errorf("LocalPath = %q, want %q", ref.LocalPath, "./local/action")
	}
}

func TestResolveUsesRef_MarketplaceSimple(t *testing.T) {
	ref, err := ResolveUsesRef("actions/checkout@v4")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if ref.Local {
		t.Error("Local = true, want false")
	}
	if ref.Owner != "actions" || ref.Repo != "checkout" || ref.Ref != "v4" {
		t.Errorf("Owner/Repo/Ref = %q/%q/%q, want %q/%q/%q", ref.Owner, ref.Repo, ref.Ref, "actions", "checkout", "v4")
	}
	if ref.Subpath != "" {
		t.Errorf("Subpath = %q, want empty", ref.Subpath)
	}
}

func TestResolveUsesRef_MarketplaceWithSubpath(t *testing.T) {
	ref, err := ResolveUsesRef("owner/repo/some/nested/action@v1")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if ref.Owner != "owner" || ref.Repo != "repo" || ref.Ref != "v1" {
		t.Errorf("Owner/Repo/Ref = %q/%q/%q, want %q/%q/%q", ref.Owner, ref.Repo, ref.Ref, "owner", "repo", "v1")
	}
	if ref.Subpath != "some/nested/action" {
		t.Errorf("Subpath = %q, want %q", ref.Subpath, "some/nested/action")
	}
}

func TestResolveUsesRef_MissingRefIsError(t *testing.T) {
	_, err := ResolveUsesRef("actions/checkout")
	if err == nil {
		t.Fatal("ResolveUsesRef() error = nil, want error for a reference missing @ref")
	}
}

func TestResolveUsesRef_MissingRepoIsError(t *testing.T) {
	_, err := ResolveUsesRef("actions@v4")
	if err == nil {
		t.Fatal("ResolveUsesRef() error = nil, want error for a reference missing owner/repo")
	}
}
