package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type TransitionPaths struct {
	TicketArtifactPath string
	ClaimArtifactPath  string
	WaveArtifactPath   string
	RunArtifactPath    string
}

type TransitionPayloads struct {
	TicketArtifact any
	ClaimArtifact  any
	WaveArtifact   any
	RunArtifact    any
}

func SaveJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func LoadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read json: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

func WriteTransitionCommit(paths TransitionPaths, payloads TransitionPayloads) error {
	sidecars := []struct {
		path    string
		payload any
	}{
		{path: paths.TicketArtifactPath, payload: payloads.TicketArtifact},
		{path: paths.ClaimArtifactPath, payload: payloads.ClaimArtifact},
		{path: paths.WaveArtifactPath, payload: payloads.WaveArtifact},
	}

	for _, sidecar := range sidecars {
		if sidecar.path == "" || sidecar.payload == nil {
			continue
		}
		if err := SaveJSONAtomic(sidecar.path, sidecar.payload); err != nil {
			return err
		}
	}

	if paths.RunArtifactPath == "" || payloads.RunArtifact == nil {
		return nil
	}
	if err := SaveJSONAtomic(paths.RunArtifactPath, payloads.RunArtifact); err != nil {
		return err
	}
	return nil
}
