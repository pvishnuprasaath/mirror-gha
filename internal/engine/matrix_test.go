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

func TestExpandMatrix_IncludeExcludeRejected(t *testing.T) {
	for _, key := range []string{"include", "exclude"} {
		strategy := &Strategy{
			Matrix: map[string]interface{}{
				"os": []interface{}{"ubuntu-latest"},
				key:  []interface{}{map[string]interface{}{"os": "ubuntu-latest", "extra": "x"}},
			},
		}
		_, err := ExpandMatrix(strategy)
		if err == nil {
			t.Errorf("ExpandMatrix() with matrix.%s error = nil, want a clear unsupported error", key)
		}
	}
}
