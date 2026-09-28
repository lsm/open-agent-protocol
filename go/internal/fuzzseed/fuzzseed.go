package fuzzseed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/go/harness"
	"github.com/lsm/open-agent-protocol/harnesses"
)

const DefaultLimit = 40

func RepositoryRoot() (string, error) {
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		return "", fmt.Errorf("fuzzseed: no caller")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "fixtures", "manifest.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("fuzzseed: no fixtures/manifest.json above %s", file)
		}
		dir = parent
	}
}

type manifestFile struct {
	Fixtures []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"fixtures"`
}

func Limited(root string, limit int, patterns ...string) ([][]byte, error) {
	paths, err := matched(root, patterns)
	if err != nil {
		return nil, err
	}
	return fromPaths(paths, limit)
}

func fromPaths(paths []string, limit int) ([][]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	seeds := make([][]byte, 0, limit)
	seen := make(map[string]bool, limit)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if len(seeds) >= limit {
				return seeds, nil
			}
			if strings.TrimSpace(line) == "" || seen[line] {
				continue
			}
			seen[line] = true
			seeds = append(seeds, []byte(line))
		}
	}
	return seeds, nil
}

func Default(root string, patterns ...string) ([][]byte, error) {
	return Limited(root, DefaultLimit, patterns...)
}

func Under(root, dir string, limit int) ([][]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	prefix := filepath.Join(root, filepath.FromSlash(dir))
	var paths []string
	err := filepath.WalkDir(prefix, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fuzzseed: walk %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("fuzzseed: %s holds no jsonl corpus", dir)
	}
	sort.Strings(paths)
	return fromPaths(paths, limit)
}

func Manifest(root string, limit int) ([][]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	paths, err := manifestPaths(root)
	if err != nil {
		return nil, err
	}
	seeds := make([][]byte, 0, limit)
	for _, index := range spread(len(paths), limit) {
		fixture, err := os.ReadFile(paths[index])
		if err != nil {
			return nil, fmt.Errorf("fuzzseed: read %s: %w", paths[index], err)
		}
		seeds = append(seeds, fixture)
	}
	return seeds, nil
}

func manifestPaths(root string) ([]string, error) {
	body, err := os.ReadFile(filepath.Join(root, "fixtures", "manifest.json"))
	if err != nil {
		return nil, err
	}
	var file manifestFile
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("fuzzseed: parse fixtures/manifest.json: %w", err)
	}
	var paths []string
	for _, entry := range file.Fixtures {
		path := filepath.Join(root, "fixtures", filepath.FromSlash(entry.Path))
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("fuzzseed: read %s: %w", entry.ID, err)
		}
		if !info.IsDir() {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("fuzzseed: fixtures/manifest.json declares no readable fixture")
	}
	return paths, nil
}

func spread(total, limit int) []int {
	step := 1
	if limit < total {
		step = total / limit
	}
	picked := make([]int, 0, limit)
	taken := make(map[int]bool, limit)
	for index := 0; index < total && len(picked) < limit; index += step {
		picked = append(picked, index)
		taken[index] = true
	}
	for index := 0; index < total && len(picked) < limit; index++ {
		if !taken[index] {
			picked = append(picked, index)
		}
	}
	return picked
}

func matched(root string, patterns []string) ([]string, error) {
	var paths []string
	for _, pattern := range patterns {
		found, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, fmt.Errorf("fuzzseed: %s: %w", pattern, err)
		}
		paths = append(paths, found...)
	}
	sort.Strings(paths)
	return paths, nil
}

func Corpus(id string, limit int) ([][]byte, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	catalog, err := harness.Load(harnesses.Files)
	if err != nil {
		return nil, fmt.Errorf("fuzzseed: load the harness catalog: %w", err)
	}
	var dirs []string
	found := false
	for _, entry := range catalog.Harnesses {
		if entry.ID != id {
			continue
		}
		found = true
		for _, version := range entry.Versions {
			if version.Status == harness.StatusRetired || version.Corpus == "" {
				continue
			}
			dirs = append(dirs, version.Corpus)
		}
	}
	if !found {
		return nil, fmt.Errorf("fuzzseed: the catalog has no harness %q", id)
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("fuzzseed: the harness %q has no corpus", id)
	}
	sort.Strings(dirs)
	var seeds [][]byte
	for _, dir := range dirs {
		bodies, err := Under(root, dir, limit)
		if err != nil {
			return nil, err
		}
		seeds = append(seeds, bodies...)
	}
	return seeds, nil
}
