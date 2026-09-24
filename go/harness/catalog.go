package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const SchemaFile = "harness.schema.json"

const schemaURL = "https://open-agent-protocol.local/harnesses/" + SchemaFile

type Status string

const (
	StatusCurrent   Status = "current"
	StatusSupported Status = "supported"
	StatusFloor     Status = "floor"
	StatusRetired   Status = "retired"
)

type Catalog struct {
	Harnesses []Harness
}

type Harness struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

type Version struct {
	Label              string      `json:"label"`
	Status             Status      `json:"status"`
	EndpointVersion    string      `json:"endpoint_version,omitempty"`
	CapabilityRevision string      `json:"capability_revision,omitempty"`
	Admits             []string    `json:"admits,omitempty"`
	Ledgers            []string    `json:"ledgers"`
	Corpus             string      `json:"corpus,omitempty"`
	CorpusFrom         string      `json:"corpus_from,omitempty"`
	Components         []Component `json:"components,omitempty"`
	Artifacts          []Artifact  `json:"artifacts,omitempty"`
	Sources            []Source    `json:"sources,omitempty"`
}

type Component struct {
	Name       string `json:"name"`
	Package    string `json:"package,omitempty"`
	Version    string `json:"version,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type Artifact struct {
	Component string `json:"component"`
	Platform  string `json:"platform"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type Source struct {
	Component string `json:"component"`
	Tag       string `json:"tag,omitempty"`
	Commit    string `json:"commit"`
	Tree      string `json:"tree,omitempty"`
}

func Load(files fs.FS) (Catalog, error) {
	schema, err := CompileSchema(files)
	if err != nil {
		return Catalog{}, err
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return Catalog{}, fmt.Errorf("read harness catalog: %w", err)
	}
	var catalog Catalog
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || path.Ext(name) != ".json" || name == SchemaFile {
			continue
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return Catalog{}, fmt.Errorf("read %s: %w", name, err)
		}
		harness, err := Parse(schema, name, data)
		if err != nil {
			return Catalog{}, err
		}
		catalog.Harnesses = append(catalog.Harnesses, harness)
	}
	if len(catalog.Harnesses) == 0 {
		return Catalog{}, errors.New("harness catalog holds no harness")
	}
	return catalog, nil
}

func CompileSchema(files fs.FS) (*jsonschema.Schema, error) {
	data, err := fs.ReadFile(files, SchemaFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", SchemaFile, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", SchemaFile, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, fmt.Errorf("register %s: %w", SchemaFile, err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", SchemaFile, err)
	}
	return schema, nil
}

func Parse(schema *jsonschema.Schema, name string, data []byte) (Harness, error) {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return Harness{}, fmt.Errorf("%s: %w", name, err)
	}
	if err := schema.Validate(instance); err != nil {
		return Harness{}, fmt.Errorf("%s: %w", name, err)
	}
	harness, err := DecodeStrict(data)
	if err != nil {
		return Harness{}, fmt.Errorf("%s: %w", name, err)
	}
	if harness.ID+".json" != name {
		return Harness{}, fmt.Errorf("%s: harness %q belongs in %s.json", name, harness.ID, harness.ID)
	}
	return harness, nil
}

func DecodeStrict(data []byte) (Harness, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var harness Harness
	if err := decoder.Decode(&harness); err != nil {
		return Harness{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Harness{}, errors.New("data after the harness object")
	}
	return harness, nil
}
