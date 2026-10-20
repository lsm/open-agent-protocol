package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

type DiscoveryMode int

const (
	DiscoverAllowCache DiscoveryMode = iota
	DiscoverForceFetch
)

const (
	ListingMaxAge        = 24 * time.Hour
	ListingFetchTimeout  = 20 * time.Second
	maxListingBytes      = 2 * 1024 * 1024
	listingContextWindow = 128_000
	listingMaxTokens     = 8_192
	keyRefusalMarker     = "key"
)

var errInvalidListing = errors.New("invalid model listing")

type DiscoveredModel struct {
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
	Reasoning     *bool
	ImageInput    *bool
}

type ListedModel struct {
	Model         Model
	Name          string
	ContextWindow int
	ImageInput    bool
	Discovered    bool
}

type Discovery struct {
	Models     []ListedModel
	KeyRefused []string
}

type Discoverer struct {
	Catalog     providercatalog.Catalog
	Environment []providercatalog.EnvironmentValue
	Stored      func(id string) *providercatalog.StoredCredential
	Client      *http.Client
	Cache       ListingCache
}

type ListingCache struct {
	Dir string
	Now func() time.Time
}

func HomeListingCache(home string) ListingCache {
	return ListingCache{Dir: filepath.Join(home, ".oapx", "model_catalog")}
}

type listingTarget struct {
	id        string
	wire      string
	baseURL   string
	modelsURL string
	region    string
}

type listing struct {
	models     []DiscoveredModel
	fetched    bool
	keyRefused bool
}

type refusal struct{ key bool }

func (refusal) Error() string { return "the models endpoint refused the credential" }

func (d Discoverer) Discover(ctx context.Context, mode DiscoveryMode) Discovery {
	var found Discovery
	for _, id := range providercatalog.LoaderOrder(d.Catalog) {
		d.discoverRow(ctx, id, mode, &found)
	}
	return found
}

func (d Discoverer) discoverRow(ctx context.Context, id string, mode DiscoveryMode, out *Discovery) {
	target, ok := d.target(id)
	if !ok {
		return
	}
	var stored *providercatalog.StoredCredential
	if d.Stored != nil {
		if stored = d.Stored(providercatalog.CredentialKey(d.Catalog, id)); stored == nil {
			stored = d.Stored(id)
		}
	}
	credential, held := providercatalog.LookupCredential(d.Catalog, d.Environment, stored, id)
	if !held {
		return
	}
	models, keyRefused, dropped := d.discoverTarget(ctx, target, credential.Key, mode, credential.Source == providercatalog.SourceStored)
	if keyRefused {
		out.KeyRefused = append(out.KeyRefused, id)
	}
	if dropped {
		return
	}
	if len(models) > 0 {
		for _, model := range models {
			out.Models = append(out.Models, d.listedModel(target, model, true))
		}
		return
	}
	for _, declared := range declaredModels(d.Catalog, id) {
		out.Models = append(out.Models, d.listedModel(target, DiscoveredModel{ID: declared.ID}, false))
	}
}

func (d Discoverer) target(id string) (listingTarget, bool) {
	region := providercatalog.ResolveRegion(d.Catalog, d.Environment, id)
	target, ok := catalogTarget(d.Catalog, id, region, "")
	if !ok {
		return listingTarget{}, false
	}
	base := d.baseOverride(id, target.wire)
	if base == "" || base == target.baseURL {
		return target, true
	}
	if moved := providercatalog.OverriddenWire(id); moved != "" && moved != target.wire {
		if retargeted, ok := catalogTarget(d.Catalog, id, region, moved); ok {
			target = retargeted
			base = d.baseOverride(id, target.wire)
		}
	}
	path := providercatalog.ModelsEndpoint(d.Catalog, id)
	carries := providercatalog.CarriesVersion(d.Catalog, id, target.baseURL)
	target.baseURL = base
	target.modelsURL = providercatalog.ModelsURLForBase(base, path, carries, true)
	return target, true
}

func (d Discoverer) baseOverride(id, wire string) string {
	given := environmentValue(d.Environment, providercatalog.GlobalBaseURLEnv)
	if given == "" {
		given = environmentValue(d.Environment, providercatalog.FirstBaseURLEnv(d.Catalog, id))
	}
	if given == "" {
		return ""
	}
	if providercatalog.UsesVersionedRoute(id, wire) {
		return providercatalog.NormalizeVersionedBaseURL(given)
	}
	return strings.TrimRight(given, "/")
}

func environmentValue(env []providercatalog.EnvironmentValue, name string) string {
	if name == "" {
		return ""
	}
	for _, held := range env {
		if held.Name == name {
			return held.Value
		}
	}
	return ""
}

func catalogTarget(catalog providercatalog.Catalog, id, region, wanted string) (listingTarget, bool) {
	chosen := ""
	for _, wire := range providercatalog.Wires(catalog, id) {
		if wanted != "" && wire != wanted {
			continue
		}
		if !loaderWire(wire) {
			continue
		}
		if _, ok := providercatalog.RequestURL(catalog, id, wire, region); !ok {
			continue
		}
		chosen = wire
		break
	}
	if chosen == "" {
		return listingTarget{}, false
	}
	base, ok := providercatalog.BaseURL(catalog, id, chosen, region)
	if !ok {
		return listingTarget{}, false
	}
	models, ok := providercatalog.ModelsURL(catalog, id, region)
	if !ok {
		return listingTarget{}, false
	}
	return listingTarget{id: id, wire: chosen, baseURL: base, modelsURL: models, region: region}, true
}

func loaderWire(wire string) bool {
	for _, implemented := range providercatalog.LoaderWires {
		if implemented == wire {
			return true
		}
	}
	return false
}

func dropsOnRefusal(catalog providercatalog.Catalog, id string) bool {
	return providercatalog.Offering(catalog, id) == "coding_plan"
}

func (d Discoverer) discoverTarget(ctx context.Context, target listingTarget, token string, mode DiscoveryMode, honour bool) ([]DiscoveredModel, bool, bool) {
	marker := refusalMarkerName(target)
	marked := honour && d.Cache.refusalRemembered(marker)
	if marked && mode == DiscoverAllowCache && d.Cache.fresh(marker, ListingMaxAge) {
		return nil, d.Cache.rememberedKeyRefusal(marker), true
	}
	found, err := d.cacheThenProbe(ctx, target, token, mode, marked)
	var refused refusal
	if errors.As(err, &refused) {
		if honour {
			d.Cache.rememberRefusal(marker, refused.key)
		}
		return nil, refused.key, true
	}
	if found.models != nil && found.fetched && honour {
		d.Cache.forget(marker)
	}
	if marked && !found.fetched {
		return nil, found.keyRefused, true
	}
	return found.models, found.keyRefused, false
}

func (d Discoverer) cacheThenProbe(ctx context.Context, target listingTarget, token string, mode DiscoveryMode, skipCache bool) (listing, error) {
	name := listingCacheName(target)
	if mode == DiscoverAllowCache && !skipCache {
		if models := d.Cache.load(name, ListingMaxAge); models != nil {
			return listing{models: models}, nil
		}
	}
	body, err := d.fetchListing(ctx, target, token)
	var refused refusal
	if errors.As(err, &refused) {
		if dropsOnRefusal(d.Catalog, target.id) {
			return listing{}, refused
		}
		found := d.cachedOrNothing(name, skipCache)
		found.keyRefused = refused.key
		return found, nil
	}
	if err != nil {
		return d.cachedOrNothing(name, skipCache), nil
	}
	if models, err := ParseModelListing(body); err == nil && len(models) > 0 {
		d.Cache.save(name, body)
		return listing{models: models, fetched: true}, nil
	}
	return d.cachedOrNothing(name, skipCache), nil
}

func (d Discoverer) cachedOrNothing(name string, skipCache bool) listing {
	if skipCache {
		return listing{}
	}
	return listing{models: d.Cache.load(name, 0)}
}

func (d Discoverer) fetchListing(ctx context.Context, target listingTarget, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ListingFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.modelsURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("accept-encoding", "identity")
	if token != "" {
		if target.wire == "anthropic-messages" {
			request.Header.Set("x-api-key", token)
			request.Header.Set("anthropic-version", "2023-06-01")
		} else {
			request.Header.Set("authorization", BearerValue(token))
		}
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, refusal{key: true}
	case http.StatusPaymentRequired:
		return nil, refusal{}
	case http.StatusOK:
	default:
		return nil, errors.New("models endpoint answered " + strconv.Itoa(response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxListingBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxListingBytes {
		return nil, errors.New("model listing exceeds its size limit")
	}
	return body, nil
}

func ParseModelListing(data []byte) ([]DiscoveredModel, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return nil, errInvalidListing
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(top["data"], &entries); err != nil || top["data"] == nil || entries == nil {
		return nil, errInvalidListing
	}
	models := []DiscoveredModel{}
	for _, entry := range entries {
		var item map[string]json.RawMessage
		if json.Unmarshal(entry, &item) != nil || item == nil {
			continue
		}
		id, ok := stringMember(item, "id")
		if !ok || id == "" {
			continue
		}
		model := DiscoveredModel{ID: id}
		if name, ok := stringMember(item, "display_name"); ok {
			model.Name = name
		}
		model.ContextWindow = firstPositive(item, "context_length", "context_window")
		model.MaxTokens = firstPositive(item, "max_output_tokens", "max_tokens")
		model.Reasoning = firstBool(item, "supports_reasoning", "reasoning")
		if model.Reasoning == nil {
			model.Reasoning = reportsEffortLevels(item)
		}
		model.ImageInput = firstBool(item, "supports_image_in", "vision")
		if model.ImageInput == nil {
			if namesImage(item, "modalities") || namesImage(item, "input_modalities") {
				model.ImageInput = boolPointer(true)
			} else if holdsList(item, "modalities") || holdsList(item, "input_modalities") {
				model.ImageInput = boolPointer(false)
			}
		}
		models = append(models, model)
	}
	return models, nil
}

func stringMember(item map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := listingMember(item, name)
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func listingMember(item map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	raw, ok := item[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	return raw, true
}

func firstPositive(item map[string]json.RawMessage, names ...string) int {
	for _, name := range names {
		raw, ok := listingMember(item, name)
		if !ok {
			continue
		}
		var value float64
		if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || value < 0 || value > math.MaxUint32 {
			continue
		}
		return int(value)
	}
	return 0
}

func firstBool(item map[string]json.RawMessage, names ...string) *bool {
	for _, name := range names {
		raw, ok := listingMember(item, name)
		if !ok {
			continue
		}
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		return &value
	}
	return nil
}

func reportsEffortLevels(item map[string]json.RawMessage) *bool {
	raw, ok := listingMember(item, "effort")
	if !ok {
		return nil
	}
	var effort map[string]json.RawMessage
	if json.Unmarshal(raw, &effort) != nil {
		return nil
	}
	levels, ok := listingMember(effort, "supported_levels")
	if !ok {
		return nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(levels, &entries) != nil || len(entries) == 0 {
		return nil
	}
	return boolPointer(true)
}

func holdsList(item map[string]json.RawMessage, name string) bool {
	raw, ok := item[name]
	if !ok {
		return false
	}
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && values != nil
}

func namesImage(item map[string]json.RawMessage, name string) bool {
	raw, ok := item[name]
	if !ok {
		return false
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	for _, value := range values {
		var text string
		if json.Unmarshal(value, &text) == nil && strings.EqualFold(text, "image") {
			return true
		}
	}
	return false
}

func boolPointer(value bool) *bool { return &value }

func declaredModels(catalog providercatalog.Catalog, id string) []providercatalog.Model {
	for _, row := range catalog.Providers {
		if row.ID == id {
			return row.Models
		}
	}
	return nil
}

func declaredModel(catalog providercatalog.Catalog, id, modelID string) (providercatalog.Model, bool) {
	for _, model := range declaredModels(catalog, id) {
		if model.ID == modelID {
			return model, true
		}
	}
	return providercatalog.Model{}, false
}

func rowFigures(catalog providercatalog.Catalog, id string) (int, int) {
	for _, row := range catalog.Providers {
		if row.ID == id {
			return row.ContextWindow, row.MaxTokens
		}
	}
	return 0, 0
}

func (d Discoverer) listedModel(target listingTarget, found DiscoveredModel, discovered bool) ListedModel {
	declared, isDeclared := declaredModel(d.Catalog, target.id, found.ID)
	rowWindow, rowTokens := rowFigures(d.Catalog, target.id)
	name := found.Name
	if name == "" {
		name = found.ID
		if isDeclared && declared.Name != "" {
			name = declared.Name
		}
	}
	window := firstNonZero(found.ContextWindow, declared.ContextWindow, rowWindow, listingContextWindow)
	tokens := firstNonZero(found.MaxTokens, declared.MaxTokens, rowTokens, listingMaxTokens)
	reasoning := found.Reasoning != nil && *found.Reasoning
	image := found.ImageInput != nil && *found.ImageInput
	return ListedModel{
		Model: Model{
			ID:         found.ID,
			API:        listedWire(d.Catalog, target, found.ID),
			Provider:   target.id,
			BaseURL:    target.baseURL,
			HasBaseURL: true,
			Reasoning:  reasoning,
			MaxTokens:  tokens,
		},
		Name:          name,
		ContextWindow: window,
		ImageInput:    image,
		Discovered:    discovered,
	}
}

func listedWire(catalog providercatalog.Catalog, target listingTarget, modelID string) string {
	for _, row := range catalog.Providers {
		if row.ID != target.id {
			continue
		}
		if providercatalog.FirstImplementedWire(row) != target.wire {
			return target.wire
		}
		return providercatalog.WireForModel(catalog, target.id, modelID)
	}
	return target.wire
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func listingCacheName(target listingTarget) string {
	if target.region != "" {
		return "catalog-" + target.id + "-" + target.region + ".json"
	}
	return "catalog-" + target.id + ".json"
}

func refusalMarkerName(target listingTarget) string {
	return strings.TrimSuffix(listingCacheName(target), ".json") + ".refused"
}

func (c ListingCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c ListingCache) path(name string) string {
	return filepath.Join(c.Dir, name)
}

func (c ListingCache) fresh(name string, maxAge time.Duration) bool {
	if c.Dir == "" {
		return false
	}
	info, err := os.Stat(c.path(name))
	if err != nil {
		return false
	}
	return c.now().Sub(info.ModTime()) <= maxAge
}

func (c ListingCache) load(name string, maxAge time.Duration) []DiscoveredModel {
	if c.Dir == "" {
		return nil
	}
	if maxAge > 0 && !c.fresh(name, maxAge) {
		return nil
	}
	file, err := os.Open(c.path(name))
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxListingBytes+1))
	if err != nil || len(data) > maxListingBytes {
		return nil
	}
	models, err := ParseModelListing(data)
	if err != nil || len(models) == 0 {
		return nil
	}
	return models
}

func (c ListingCache) save(name string, body []byte) {
	if c.Dir == "" {
		return
	}
	if os.MkdirAll(c.Dir, 0o700) != nil {
		return
	}
	temporary, err := os.CreateTemp(c.Dir, name+".tmp.*")
	if err != nil {
		return
	}
	_, writeErr := temporary.Write(body)
	closeErr := temporary.Close()
	if writeErr != nil || closeErr != nil || os.Rename(temporary.Name(), c.path(name)) != nil {
		os.Remove(temporary.Name())
	}
}

func (c ListingCache) refusalRemembered(name string) bool {
	if c.Dir == "" {
		return false
	}
	info, err := os.Stat(c.path(name))
	return err == nil && info.Mode().IsRegular()
}

func (c ListingCache) rememberedKeyRefusal(name string) bool {
	if c.Dir == "" {
		return false
	}
	data, err := os.ReadFile(c.path(name))
	return err == nil && string(data) == keyRefusalMarker
}

func (c ListingCache) rememberRefusal(name string, key bool) {
	if c.Dir == "" {
		return
	}
	if os.MkdirAll(c.Dir, 0o700) != nil {
		return
	}
	body := ""
	if key {
		body = keyRefusalMarker
	}
	os.WriteFile(c.path(name), []byte(body), 0o600)
}

func (c ListingCache) forget(name string) {
	if c.Dir == "" {
		return
	}
	os.Remove(c.path(name))
}
