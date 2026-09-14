package engine

import (
	"fmt"
	"sort"
)

// MatrixCombination is one concrete assignment of matrix variables for a
// single matrix job instance, e.g. {"os": "ubuntu-latest", "version": 18}.
type MatrixCombination map[string]interface{}

// ExpandMatrix turns a job's strategy.matrix definition into the list of
// concrete combinations to run — the cartesian product of each axis. A
// strategy with no matrix (or a nil strategy) produces exactly one empty
// combination, meaning "run the job once, no matrix variables."
//
// matrix.include and matrix.exclude are parsed as ordinary matrix keys and
// rejected with a clear error rather than approximated: GitHub Actions'
// merge semantics for them are fiddly enough that a partial implementation
// risks silently producing the wrong job set, which is worse than refusing.
func ExpandMatrix(strategy *Strategy) ([]MatrixCombination, error) {
	if strategy == nil || strategy.Matrix == nil {
		return []MatrixCombination{{}}, nil
	}
	if _, ok := strategy.Matrix["include"]; ok {
		return nil, fmt.Errorf("matrix.include is not supported yet")
	}
	if _, ok := strategy.Matrix["exclude"]; ok {
		return nil, fmt.Errorf("matrix.exclude is not supported yet")
	}

	axes := map[string][]interface{}{}
	keys := make([]string, 0, len(strategy.Matrix))
	for k, v := range strategy.Matrix {
		list, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("matrix axis %q must be a list", k)
		}
		axes[k] = list
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic combination ordering

	combos := []MatrixCombination{{}}
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
	return combos, nil
}
