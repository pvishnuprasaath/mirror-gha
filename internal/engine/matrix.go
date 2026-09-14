package engine

import (
	"fmt"
	"reflect"
	"sort"
)

// MatrixCombination is one concrete assignment of matrix variables for a
// single matrix job instance, e.g. {"os": "ubuntu-latest", "version": 18}.
type MatrixCombination map[string]interface{}

// ExpandMatrix turns a job's strategy.matrix definition into the list of
// concrete combinations to run. A strategy with no matrix (or a nil
// strategy) produces exactly one empty combination, meaning "run the job
// once, no matrix variables."
//
// include/exclude semantics match act's real implementation
// (pkg/model/workflow.go's GetMatrixes, commonKeysMatch/commonKeysMatch2):
// build the cartesian product of the remaining axes, drop any combination
// matching ALL key/value pairs of an exclude entry (every exclude key must
// be a real axis key, or this is an error), then for each include entry
// merge its non-axis keys into every combination whose axis-key subset
// already matches — an include entry that matches nothing becomes its own
// standalone combination. A matrix with only include and no axes produces
// zero combinations from the (empty) cartesian product, so every include
// entry falls through to becoming its own standalone combination.
func ExpandMatrix(strategy *Strategy) ([]MatrixCombination, error) {
	if strategy == nil || strategy.Matrix == nil {
		return []MatrixCombination{{}}, nil
	}

	axes := map[string][]interface{}{}
	var includeEntries, excludeEntries []map[string]interface{}
	keys := make([]string, 0, len(strategy.Matrix))
	for k, v := range strategy.Matrix {
		switch k {
		case "include":
			entries, err := matrixEntryList(v)
			if err != nil {
				return nil, fmt.Errorf("matrix.include: %w", err)
			}
			includeEntries = entries
			continue
		case "exclude":
			entries, err := matrixEntryList(v)
			if err != nil {
				return nil, fmt.Errorf("matrix.exclude: %w", err)
			}
			excludeEntries = entries
			continue
		}
		list, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("matrix axis %q must be a list", k)
		}
		axes[k] = list
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic combination ordering

	for _, exclude := range excludeEntries {
		for k := range exclude {
			if _, ok := axes[k]; !ok {
				return nil, fmt.Errorf("matrix exclude key %q does not match any key within the matrix", k)
			}
		}
	}

	var combos []MatrixCombination
	if len(keys) > 0 {
		combos = []MatrixCombination{{}}
		for _, k := range keys {
			var next []MatrixCombination
			for _, c := range combos {
				for _, v := range axes[k] {
					nc := make(MatrixCombination, len(c)+1)
					for ek, ev := range c {
						nc[ek] = ev
					}
					nc[k] = v
					next = append(next, nc)
				}
			}
			combos = next
		}
	}

	combos = applyMatrixExclude(combos, excludeEntries)
	combos = applyMatrixInclude(combos, includeEntries)

	return combos, nil
}

// matrixEntryList normalizes an include/exclude value into a list of
// entries — GitHub Actions accepts either a list of mappings or a single
// bare mapping.
func matrixEntryList(v interface{}) ([]map[string]interface{}, error) {
	switch vv := v.(type) {
	case []interface{}:
		entries := make([]map[string]interface{}, 0, len(vv))
		for _, item := range vv {
			m, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("entry must be a mapping, got %T", item)
			}
			entries = append(entries, m)
		}
		return entries, nil
	case map[string]interface{}:
		return []map[string]interface{}{vv}, nil
	default:
		return nil, fmt.Errorf("must be a list or a mapping, got %T", v)
	}
}

func applyMatrixExclude(combos []MatrixCombination, excludes []map[string]interface{}) []MatrixCombination {
	if len(excludes) == 0 {
		return combos
	}
	var kept []MatrixCombination
	for _, c := range combos {
		excluded := false
		for _, exclude := range excludes {
			if matrixAllMatch(c, exclude) {
				excluded = true
				break
			}
		}
		if !excluded {
			kept = append(kept, c)
		}
	}
	return kept
}

func applyMatrixInclude(combos []MatrixCombination, includes []map[string]interface{}) []MatrixCombination {
	for _, include := range includes {
		matched := false
		for _, c := range combos {
			if matrixSubsetMatch(c, include) {
				matched = true
				for k, v := range include {
					c[k] = v
				}
			}
		}
		if !matched {
			nc := make(MatrixCombination, len(include))
			for k, v := range include {
				nc[k] = v
			}
			combos = append(combos, nc)
		}
	}
	return combos
}

// matrixAllMatch reports whether every key/value pair in entry matches
// combo — used for exclude, where every exclude key is already validated
// to be a real axis key.
func matrixAllMatch(combo MatrixCombination, entry map[string]interface{}) bool {
	for k, v := range entry {
		cv, ok := combo[k]
		if !ok || !reflect.DeepEqual(cv, v) {
			return false
		}
	}
	return true
}

// matrixSubsetMatch reports whether entry's keys that are ALSO present in
// combo all match — matches act's include semantics: keys the include
// entry introduces that aren't existing axes are ignored for matching and
// only merged in afterward.
func matrixSubsetMatch(combo MatrixCombination, entry map[string]interface{}) bool {
	for k, v := range entry {
		if cv, ok := combo[k]; ok && !reflect.DeepEqual(cv, v) {
			return false
		}
	}
	return true
}
