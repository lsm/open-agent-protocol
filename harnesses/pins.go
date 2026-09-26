package harnesses

import (
	"encoding/json"
	"fmt"
)

type Pin struct {
	Label              string      `json:"label"`
	Status             string      `json:"status"`
	EndpointVersion    string      `json:"endpoint_version"`
	CapabilityRevision string      `json:"capability_revision"`
	Admits             []string    `json:"admits"`
	Corpus             string      `json:"corpus"`
	Components         []Component `json:"components"`
	Artifacts          []Artifact  `json:"artifacts"`
	Sources            []Source    `json:"sources"`
}

type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Artifact struct {
	Component string `json:"component"`
	Platform  string `json:"platform"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
}

type Source struct {
	Component string `json:"component"`
	Tag       string `json:"tag"`
	Commit    string `json:"commit"`
	Tree      string `json:"tree"`
}

func Current(id string) Pin {
	return Versioned(id, "current")
}

func Versioned(id, status string) Pin {
	data, err := Files.ReadFile(id + ".json")
	if err != nil {
		panic(fmt.Sprintf("harnesses: %v", err))
	}
	var entry struct {
		Versions []Pin `json:"versions"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		panic(fmt.Sprintf("harnesses: %s: %v", id, err))
	}
	for _, version := range entry.Versions {
		if version.Status == status {
			return version
		}
	}
	panic(fmt.Sprintf("harnesses: %s has no %s version", id, status))
}

func (p Pin) Source(component string) Source {
	for _, source := range p.Sources {
		if source.Component == component {
			return source
		}
	}
	panic(fmt.Sprintf("harnesses: %s has no source %q", p.Label, component))
}

func (p Pin) Component(name string) Component {
	for _, component := range p.Components {
		if component.Name == name {
			return component
		}
	}
	panic(fmt.Sprintf("harnesses: %s has no component %q", p.Label, name))
}

func (p Pin) Artifact(component, platform, kind string) Artifact {
	for _, artifact := range p.Artifacts {
		if artifact.Component == component && artifact.Platform == platform && artifact.Kind == kind {
			return artifact
		}
	}
	panic(fmt.Sprintf("harnesses: %s has no %s %s artifact for %s", p.Label, component, kind, platform))
}
