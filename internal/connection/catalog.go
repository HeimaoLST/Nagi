package connection

import (
	"cmp"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed catalog/providers.json catalog/icons/*.svg
var providerCatalogFiles embed.FS

const providerIconLimit = 64 << 10

type LocalizedText map[string]string

func (text LocalizedText) Resolve(locale string) string {
	if value := text[locale]; value != "" {
		return value
	}
	for prefix := locale; prefix != ""; {
		if cut := strings.LastIndexByte(prefix, '-'); cut >= 0 {
			prefix = prefix[:cut]
			if value := text[prefix]; value != "" {
				return value
			}
		} else {
			break
		}
	}
	tags := slices.Sorted(maps.Keys(text))
	language, _, _ := strings.Cut(locale, "-")
	for _, tag := range tags {
		if strings.HasPrefix(tag, language+"-") && text[tag] != "" {
			return text[tag]
		}
	}
	if value := text["en"]; value != "" {
		return value
	}
	for _, tag := range tags {
		if text[tag] != "" {
			return text[tag]
		}
	}
	return ""
}

type ProviderSection struct {
	ID          string        `json:"id"`
	Order       int           `json:"order"`
	Collapsible bool          `json:"collapsible"`
	Title       LocalizedText `json:"title"`
	Subtitle    LocalizedText `json:"subtitle"`
}

type ProviderPreset struct {
	ID            string        `json:"id"`
	Section       string        `json:"section"`
	Order         int           `json:"order"`
	Name          LocalizedText `json:"name"`
	Tag           LocalizedText `json:"tag"`
	Icon          string        `json:"icon"`
	IconSVG       []byte        `json:"-"`
	Endpoint      string        `json:"endpoint"`
	WireAPI       string        `json:"wireApi"`
	DefaultModel  string        `json:"defaultModel"`
	ContextTokens int           `json:"contextTokens"`
	OutputTokens  int           `json:"outputTokens"`
	Website       string        `json:"website"`
	NoKey         bool          `json:"noKey"`
	Custom        bool          `json:"custom"`
	MatchHosts    []string      `json:"matchHosts"`
	Hidden        bool          `json:"hidden"`
}

type ProviderCatalog struct {
	Sections        []ProviderSection
	Presets         []ProviderPreset
	FallbackIconSVG []byte
	Warnings        []string
}

func LoadProviderCatalog(profileDir string) ProviderCatalog {
	var catalog ProviderCatalog
	catalog.FallbackIconSVG, _ = providerCatalogFiles.ReadFile("catalog/icons/custom.svg")
	sections := make(map[string]ProviderSection)
	presets := make(map[string]ProviderPreset)
	merge := func(origin, dir string, data []byte, err error) {
		if err != nil {
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("%s: %v", origin, err))
			return
		}
		catalog.mergeLayer(origin, dir, data, sections, presets)
	}
	data, err := providerCatalogFiles.ReadFile("catalog/providers.json")
	merge("built-in providers", "", data, err)
	if profileDir != "" {
		plugins := filepath.Join(profileDir, "provider-plugins")
		entries, err := os.ReadDir(plugins)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			catalog.Warnings = append(catalog.Warnings, err.Error())
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(plugins, entry.Name())
			path := filepath.Join(dir, "providers.json")
			data, err := os.ReadFile(path)
			merge(path, dir, data, err)
		}
		path := filepath.Join(profileDir, "providers.json")
		if data, err := os.ReadFile(path); !errors.Is(err, fs.ErrNotExist) {
			merge(path, profileDir, data, err)
		}
	}
	catalog.Sections = slices.Collect(maps.Values(sections))
	slices.SortFunc(catalog.Sections, func(a, b ProviderSection) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.ID, b.ID))
	})
	sectionOrder := make(map[string]int, len(sections))
	for i, section := range catalog.Sections {
		sectionOrder[section.ID] = i
	}
	for _, preset := range presets {
		if !preset.Hidden {
			catalog.Presets = append(catalog.Presets, preset)
		}
	}
	slices.SortFunc(catalog.Presets, func(a, b ProviderPreset) int {
		return cmp.Or(cmp.Compare(sectionOrder[a.Section], sectionOrder[b.Section]), cmp.Compare(a.Order, b.Order), cmp.Compare(a.ID, b.ID))
	})
	return catalog
}

type providerCatalogLayer struct {
	Version   int               `json:"version"`
	Sections  []json.RawMessage `json:"sections"`
	Providers []json.RawMessage `json:"providers"`
}

func (catalog *ProviderCatalog) mergeLayer(origin, dir string, data []byte, sections map[string]ProviderSection, presets map[string]ProviderPreset) {
	warn := func(err error) {
		catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("%s: %v", origin, err))
	}
	var layer providerCatalogLayer
	if err := json.Unmarshal(data, &layer); err != nil {
		warn(err)
		return
	}
	if layer.Version != 1 {
		warn(fmt.Errorf("unsupported provider catalog version %d", layer.Version))
		return
	}
	for _, raw := range layer.Sections {
		id, err := providerCatalogID(raw)
		if err != nil {
			warn(err)
			continue
		}
		section := sections[id]
		section.Title, section.Subtitle = maps.Clone(section.Title), maps.Clone(section.Subtitle)
		if err := json.Unmarshal(raw, &section); err != nil {
			warn(err)
		} else if section.Title.Resolve("en") == "" {
			warn(fmt.Errorf("section %q requires a title", id))
		} else {
			sections[id] = section
		}
	}
	for _, raw := range layer.Providers {
		id, err := providerCatalogID(raw)
		if err != nil {
			warn(err)
			continue
		}
		previous := presets[id]
		preset := previous
		preset.Name, preset.Tag = maps.Clone(preset.Name), maps.Clone(preset.Tag)
		preset.MatchHosts = slices.Clone(preset.MatchHosts)
		if err := json.Unmarshal(raw, &preset); err != nil {
			warn(err)
			continue
		}
		if preset.WireAPI != "" && preset.WireAPI != "openai-completions" && preset.WireAPI != "openai-responses" {
			warn(fmt.Errorf("provider %q has an unsupported wire API", id))
			continue
		}
		if preset.Icon != "" && (!strings.HasSuffix(preset.Icon, ".svg") || strings.ContainsAny(preset.Icon, `/\`) || strings.Contains(preset.Icon, "..")) {
			warn(fmt.Errorf("provider %q has an invalid icon filename", id))
			continue
		}
		if !preset.Hidden && (sections[preset.Section].ID == "" || preset.Name.Resolve("en") == "") {
			warn(fmt.Errorf("provider %q requires a name and a known section", id))
			continue
		}
		if preset.Icon != "" {
			icon, err := readProviderIcon(dir, preset.Icon)
			if err == nil {
				preset.IconSVG = icon
			} else if !errors.Is(err, fs.ErrNotExist) {
				warn(fmt.Errorf("provider %q icon: %w", id, err))
				preset.IconSVG = catalog.FallbackIconSVG
			} else if preset.Icon != previous.Icon || len(preset.IconSVG) == 0 {
				preset.IconSVG, _ = providerCatalogFiles.ReadFile("catalog/icons/" + preset.Icon)
			}
		} else {
			preset.IconSVG = nil
		}
		if len(preset.IconSVG) == 0 {
			preset.IconSVG = catalog.FallbackIconSVG
		}
		presets[id] = preset
	}
}

func providerCatalogID(raw json.RawMessage) (string, error) {
	var entry struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", err
	}
	if strings.TrimSpace(entry.ID) == "" || strings.ContainsRune(entry.ID, 0) {
		return "", errors.New("provider catalog entry requires an ID")
	}
	return entry.ID, nil
}

func readProviderIcon(dir, name string) ([]byte, error) {
	if dir == "" {
		return nil, fs.ErrNotExist
	}
	rootPath := filepath.Join(dir, "icons")
	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("icons must be a directory inside the catalog layer")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > providerIconLimit {
		return nil, errors.New("icon must be a regular SVG of at most 64 KiB")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, providerIconLimit+1))
	if len(data) > providerIconLimit {
		return nil, errors.New("icon exceeds 64 KiB")
	}
	return data, err
}

func (catalog ProviderCatalog) SectionProviders(sectionID string) []ProviderPreset {
	var providers []ProviderPreset
	for _, preset := range catalog.Presets {
		if preset.Section == sectionID {
			providers = append(providers, preset)
		}
	}
	return providers
}

func (catalog ProviderCatalog) PresetByID(id string) (ProviderPreset, bool) {
	for _, preset := range catalog.Presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return ProviderPreset{}, false
}

func (catalog ProviderCatalog) MatchAccount(baseURL, name string) (ProviderPreset, bool) {
	if endpoint, err := url.Parse(baseURL); err == nil && endpoint.Hostname() != "" {
		host := strings.ToLower(strings.TrimSuffix(endpoint.Hostname(), "."))
		port := endpoint.Port()
		for _, preset := range catalog.Presets {
			if preset.Custom {
				continue
			}
			for _, match := range preset.MatchHosts {
				if matchesProviderHost(host, port, match) {
					return preset, true
				}
			}
			if target, err := url.Parse(preset.Endpoint); err == nil && matchesProviderHost(host, port, target.Host) {
				return preset, true
			}
		}
	}
	name = strings.TrimSpace(name)
	if name != "" {
		for _, preset := range catalog.Presets {
			if preset.Custom {
				continue
			}
			if strings.EqualFold(name, preset.ID) {
				return preset, true
			}
			for _, label := range preset.Name {
				if strings.EqualFold(name, label) {
					return preset, true
				}
			}
		}
	}
	return ProviderPreset{}, false
}

func matchesProviderHost(host, port, match string) bool {
	if matchHost, matchPort, err := net.SplitHostPort(match); err == nil {
		return strings.EqualFold(host, strings.TrimSuffix(matchHost, ".")) && port == matchPort
	}
	return strings.EqualFold(host, strings.TrimSuffix(strings.Trim(match, "[]"), "."))
}
