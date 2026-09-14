package engine

import "testing"

func TestExpandMatrix_NoStrategy(t *testing.T) {
	combos, err := ExpandMatrix(nil)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 1 || len(combos[0]) != 0 {
		t.Fatalf("ExpandMatrix(nil) = %v, want one empty combination", combos)
	}
}

func TestExpandMatrix_SingleAxis(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"version": []interface{}{"16", "18", "20"},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 3 {
		t.Fatalf("len(combos) = %d, want 3", len(combos))
	}
}

func TestExpandMatrix_CartesianProduct(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"os":      []interface{}{"ubuntu-latest", "macos-latest"},
			"version": []interface{}{"16", "18"},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 4 {
		t.Fatalf("len(combos) = %d, want 4 (2x2 cartesian product)", len(combos))
	}
	seen := map[string]bool{}
	for _, c := range combos {
		key := c["os"].(string) + "/" + c["version"].(string)
		if seen[key] {
			t.Errorf("duplicate combination %s", key)
		}
		seen[key] = true
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct combinations, got %d", len(seen))
	}
}

func TestExpandMatrix_IncludeMergesIntoMatchingCombo(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"os": []interface{}{"ubuntu-latest", "windows-latest"},
			"include": []interface{}{
				map[string]interface{}{"os": "ubuntu-latest", "extra": "x"},
			},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 2 {
		t.Fatalf("len(combos) = %d, want 2 (include merges, doesn't add a combo)", len(combos))
	}
	for _, c := range combos {
		if c["os"] == "ubuntu-latest" {
			if c["extra"] != "x" {
				t.Errorf("ubuntu-latest combo = %v, want extra=x merged in", c)
			}
		} else if _, ok := c["extra"]; ok {
			t.Errorf("windows-latest combo = %v, want no extra key", c)
		}
	}
}

func TestExpandMatrix_IncludeMatchesMultipleCombos(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"os":      []interface{}{"ubuntu-latest", "windows-latest"},
			"version": []interface{}{"16", "18"},
			"include": []interface{}{
				map[string]interface{}{"os": "ubuntu-latest", "extra": "x"},
			},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 4 {
		t.Fatalf("len(combos) = %d, want 4 (include merges into both matching combos, adds none)", len(combos))
	}
	matched := 0
	for _, c := range combos {
		if c["os"] == "ubuntu-latest" {
			if c["extra"] != "x" {
				t.Errorf("ubuntu-latest combo %v missing extra=x", c)
			}
			matched++
		}
	}
	if matched != 2 {
		t.Errorf("expected include to merge into 2 ubuntu-latest combos, matched %d", matched)
	}
}

func TestExpandMatrix_IncludeWithNoMatchBecomesStandaloneCombo(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"os": []interface{}{"ubuntu-latest"},
			"include": []interface{}{
				map[string]interface{}{"os": "macos-latest", "extra": "y"},
			},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 2 {
		t.Fatalf("len(combos) = %d, want 2 (1 axis combo + 1 standalone include)", len(combos))
	}
	found := false
	for _, c := range combos {
		if c["os"] == "macos-latest" && c["extra"] == "y" {
			found = true
		}
	}
	if !found {
		t.Errorf("combos = %v, want a standalone {os: macos-latest, extra: y} combo", combos)
	}
}

func TestExpandMatrix_ExcludeDropsMatchingCombo(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"os":      []interface{}{"ubuntu-latest", "windows-latest"},
			"version": []interface{}{"16", "18"},
			"exclude": []interface{}{
				map[string]interface{}{"os": "windows-latest", "version": "16"},
			},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 3 {
		t.Fatalf("len(combos) = %d, want 3 (4 - 1 excluded)", len(combos))
	}
	for _, c := range combos {
		if c["os"] == "windows-latest" && c["version"] == "16" {
			t.Errorf("combos = %v, want windows-latest/16 excluded", combos)
		}
	}
}

func TestExpandMatrix_ExcludeUnknownKeyErrors(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"os": []interface{}{"ubuntu-latest"},
			"exclude": []interface{}{
				map[string]interface{}{"version": "16"},
			},
		},
	}
	_, err := ExpandMatrix(strategy)
	if err == nil {
		t.Fatal("ExpandMatrix() error = nil, want error for exclude key not matching any matrix axis")
	}
}

func TestExpandMatrix_IncludeOnlyNoAxes(t *testing.T) {
	strategy := &Strategy{
		Matrix: map[string]interface{}{
			"include": []interface{}{
				map[string]interface{}{"a": "1", "b": "2"},
				map[string]interface{}{"a": "3", "b": "4"},
			},
		},
	}
	combos, err := ExpandMatrix(strategy)
	if err != nil {
		t.Fatalf("ExpandMatrix() error = %v", err)
	}
	if len(combos) != 2 {
		t.Fatalf("len(combos) = %d, want 2 (each include entry is its own combo)", len(combos))
	}
	if combos[0]["a"] != "1" || combos[0]["b"] != "2" || combos[1]["a"] != "3" || combos[1]["b"] != "4" {
		t.Errorf("combos = %v, want [{a:1,b:2}, {a:3,b:4}] in order", combos)
	}
}
