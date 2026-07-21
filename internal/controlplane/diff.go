package controlplane

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/user/randproxy/internal/config"
)

func cloneConfig(source *config.Config) (*config.Config, error) {
	if source == nil {
		return nil, fmt.Errorf("controlplane: config is required")
	}
	data, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("controlplane: marshal config clone: %w", err)
	}
	var cloned config.Config
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, fmt.Errorf("controlplane: unmarshal config clone: %w", err)
	}
	return &cloned, nil
}

func cloneConfigOrPanic(source *config.Config) *config.Config {
	cloned, err := cloneConfig(source)
	if err != nil {
		panic(err)
	}
	return cloned
}

func diffConfigPaths(current *config.Config, desired *config.Config) ([]string, error) {
	currentDoc, err := configDocument(current)
	if err != nil {
		return nil, err
	}
	desiredDoc, err := configDocument(desired)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	collectDiffPaths("", currentDoc, desiredDoc, &paths)
	sort.Strings(paths)
	return paths, nil
}

func configDocument(cfg *config.Config) (map[string]any, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("controlplane: marshal config document: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("controlplane: unmarshal config document: %w", err)
	}
	return doc, nil
}

func collectDiffPaths(prefix string, current any, desired any, paths *[]string) {
	currentMap, currentIsMap := current.(map[string]any)
	desiredMap, desiredIsMap := desired.(map[string]any)
	if currentIsMap && desiredIsMap {
		keys := make([]string, 0, len(desiredMap))
		for key := range desiredMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			nextPrefix := key
			if prefix != "" {
				nextPrefix = prefix + "." + key
			}
			collectDiffPaths(nextPrefix, currentMap[key], desiredMap[key], paths)
		}
		return
	}
	if reflect.DeepEqual(current, desired) {
		return
	}
	if prefix != "" {
		*paths = append(*paths, prefix)
	}
}
