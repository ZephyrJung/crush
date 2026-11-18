package config

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/shellconfig"
	powernapConfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/qjebbs/go-jsons"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// legacyModelPricingFields maps catwalk v2 model cost fields onto their
// v3 pricing counterparts.
var legacyModelPricingFields = map[string]string{
	"cost_per_1m_in":         "input",
	"cost_per_1m_out":        "output",
	"cost_per_1m_in_cached":  "cache_create",
	"cost_per_1m_out_cached": "cache_hit",
}

// migrateCatwalkV2 rewrites a merged user config from the catwalk v2
// schema onto the v3 schema so pre-migration configs decode correctly.
// In v3, flat model fields moved into nested structures: the cost
// fields became pricing, supports_attachments became
// capabilities.vision, and the can_reason/reasoning_levels/
// default_reasoning_effort trio became the reasoning object. The
// rewrite is pure and is a no-op for configs already written with v3
// fields; use migrateCatwalkV2File to persist the result.
func migrateCatwalkV2(data []byte) ([]byte, bool) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return data, false
	}
	providers, ok := root["providers"].(map[string]any)
	if !ok {
		return data, false
	}

	changed := false
	for _, raw := range providers {
		provider, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"models", "chatgpt_models"} {
			models, ok := provider[key].([]any)
			if !ok {
				continue
			}
			for i, rawModel := range models {
				model, ok := rawModel.(map[string]any)
				if !ok {
					continue
				}
				if migrateCatwalkV2Model(model) {
					models[i] = model
					changed = true
				}
			}
		}
	}
	if !changed {
		return data, false
	}

	out, err := json.Marshal(root)
	if err != nil {
		return data, false
	}
	return out, true
}

// migrateCatwalkV2File migrates a JSON config file from the catwalk v2
// model schema onto v3 and persists the rewrite atomically, preserving
// the file's permissions. The returned bytes are always the migrated
// form, so loading continues even when the write fails.
func migrateCatwalkV2File(path string, data []byte) []byte {
	migrated, changed := migrateCatwalkV2(data)
	if !changed {
		return data
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	if err := atomicWriteFile(path, migrated, perm); err != nil {
		slog.Warn("Failed to write migrated config", "path", path, "error", err)
	} else {
		slog.Info("Migrated config file to the catwalk v3 model schema", "path", path)
	}
	return migrated
}

// migrateCatwalkV2Model migrates a single model object in place and
// reports whether anything changed. Legacy fields are removed after
// being folded into their v3 counterparts so they never leak back into
// the decoded config.
func migrateCatwalkV2Model(model map[string]any) bool {
	changed := false

	if _, ok := model["pricing"]; !ok {
		pricing := map[string]any{}
		for legacy, field := range legacyModelPricingFields {
			value, ok := model[legacy]
			if !ok {
				continue
			}
			pricing[field] = value
			delete(model, legacy)
			changed = true
		}
		if len(pricing) > 0 {
			model["pricing"] = pricing
		}
	} else {
		for legacy := range legacyModelPricingFields {
			if _, ok := model[legacy]; ok {
				delete(model, legacy)
				changed = true
			}
		}
	}

	if value, ok := model["supports_attachments"]; ok {
		delete(model, "supports_attachments")
		capabilities, ok := model["capabilities"].(map[string]any)
		if !ok {
			capabilities = map[string]any{}
			model["capabilities"] = capabilities
		}
		if _, ok := capabilities["vision"]; !ok {
			capabilities["vision"] = value
		}
		changed = true
	}

	canReason, hasCanReason := model["can_reason"]
	levels, hasLevels := model["reasoning_levels"]
	defaultEffort, hasDefaultEffort := model["default_reasoning_effort"]
	for _, legacy := range []string{"can_reason", "reasoning_levels", "default_reasoning_effort"} {
		if _, ok := model[legacy]; ok {
			delete(model, legacy)
			changed = true
		}
	}
	if _, ok := model["reasoning"]; !ok && (hasCanReason || hasLevels || hasDefaultEffort) {
		reasoning := map[string]any{}
		if can, ok := canReason.(bool); ok && !can {
			reasoning["thinking"] = catwalk.ThinkingNever
		} else {
			reasoning["thinking"] = catwalk.ThinkingToggleable
			if raw, ok := levels.([]any); ok && len(raw) > 0 {
				values := make([]string, 0, len(raw))
				for _, level := range raw {
					if value, ok := level.(string); ok {
						values = append(values, value)
					}
				}
				if len(values) > 0 {
					effortLevels := make([]any, 0, len(values))
					for _, level := range catwalk.NewEffortLevels(values...) {
						effortLevels = append(effortLevels, map[string]any{
							"value":   level.Value,
							"display": level.Display,
						})
					}
					reasoning["effort_levels"] = effortLevels
					if effort, ok := defaultEffort.(string); ok && effort != "" {
						reasoning["default_effort_level"] = effort
					}
				}
			}
		}
		model["reasoning"] = reasoning
	}

	return changed
}

// Load loads the configuration from the default paths and returns a
// ConfigStore that owns both the pure-data Config and all runtime state.
func Load(ctx context.Context, workingDir, dataDir string, debug bool) (*ConfigStore, error) {
	// The CRUSH_DATA_DIR env var overrides the per-project data directory
	// when no --data-dir flag is given. Relative paths resolve against the
	// working directory, like the data_directory config option.
	dataDir = cmp.Or(dataDir, os.Getenv("CRUSH_DATA_DIR"))

	// Migrate deprecated disable_notifications before loading config.
	migrateDisableNotifications()

	configPaths := lookupConfigs(workingDir)

	cfg, loadedPaths, err := loadFromConfigPaths(ctx, configPaths)
	if err != nil {
		return nil, fmt.Errorf("failed to load config from paths %v: %w", configPaths, err)
	}

	cfg.setDefaults(workingDir, dataDir)

	store := &ConfigStore{
		config:         cfg,
		workingDir:     workingDir,
		globalDataPath: GlobalConfigData(),
		workspacePath:  filepath.Join(cfg.Options.DataDirectory, fmt.Sprintf("%s.json", appName)),
		loadedPaths:    loadedPaths,
	}

	if debug {
		cfg.Options.Debug = true
	}

	// Load workspace config last so it has highest priority.
	if wsData, err := readFile(store.workspacePath); err == nil && len(wsData) > 0 {
		if !json.Valid(wsData) {
			return nil, fmt.Errorf("invalid JSON in config file %s", store.workspacePath)
		}
		wsData = migrateCatwalkV2File(store.workspacePath, wsData)
		merged, mergeErr := loadFromBytes(append([][]byte{mustMarshalConfig(cfg)}, wsData))
		if mergeErr == nil {
			// Preserve defaults that setDefaults already applied.
			dataDir := cfg.Options.DataDirectory
			*cfg = *merged
			cfg.setDefaults(workingDir, dataDir)
			store.config = cfg
			store.loadedPaths = append(store.loadedPaths, store.workspacePath)
		}
	}

	// Validate hooks after all config merging is complete so workspace
	// hooks also get their matcher regexes compiled.
	if err := cfg.ValidateHooks(); err != nil {
		return nil, fmt.Errorf("invalid hook configuration: %w", err)
	}

	if !isInsideWorktree(workingDir) {
		const depth = 2
		const items = 100
		slog.Warn("No git repository detected in working directory, will limit file walk operations", "depth", depth, "items", items)
		assignIfNil(&cfg.Tools.Ls.MaxDepth, depth)
		assignIfNil(&cfg.Tools.Ls.MaxItems, items)
		assignIfNil(&cfg.Options.TUI.Completions.MaxDepth, depth)
		assignIfNil(&cfg.Options.TUI.Completions.MaxItems, items)
	}

	if isAppleTerminal() {
		slog.Warn("Detected Apple Terminal, enabling transparent mode")
		assignIfNil(&cfg.Options.TUI.Transparent, true)
	}

	// Crush uses a single built-in Ollama provider; there is no external
	// catalog to fetch and no credentials to refresh.
	providers := BuiltinProviders()
	store.knownProviders = providers

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	env := env.New()
	// Configure providers
	valueResolver := NewShellVariableResolver(env, WithWorkingDir(workingDir))
	store.resolver = valueResolver

	// Hold writeMu during initial load to prevent configureProviders
	// from triggering auto-reload via RemoveConfigField.
	store.writeMu.Lock()
	defer store.writeMu.Unlock()

	// Apply top-level env vars before configuring providers so variables
	// like AWS_PROFILE are visible to the AWS SDK credential chain.
	cfg.applyEnv(valueResolver)

	if err := cfg.configureProviders(ctx, store, env, valueResolver, store.knownProviders); err != nil {
		return nil, fmt.Errorf("failed to configure providers: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !cfg.IsConfigured() {
		slog.Warn("No providers configured")
		return store, nil
	}

	resolved, err := resolveSelectedModels(cfg, store.knownProviders)
	if err != nil {
		return nil, fmt.Errorf("failed to configure selected models: %w", err)
	}
	cfg.Models[SelectedModelTypeLarge] = resolved.Large
	cfg.Models[SelectedModelTypeSmall] = resolved.Small

	// Persist any fallback corrections while we still hold writeMu.
	if resolved.LargeFallback {
		if err := store.updateLocked(ScopeGlobal, func(c *Config) map[string]any {
			return store.updatePreferredModelFields(c, SelectedModelTypeLarge, resolved.Large)
		}); err != nil {
			return nil, fmt.Errorf("failed to update preferred large model: %w", err)
		}
	}
	if resolved.SmallFallback {
		if err := store.updateLocked(ScopeGlobal, func(c *Config) map[string]any {
			return store.updatePreferredModelFields(c, SelectedModelTypeSmall, resolved.Small)
		}); err != nil {
			return nil, fmt.Errorf("failed to update preferred small model: %w", err)
		}
	}
	store.SetupAgents()

	// Capture initial staleness snapshot. Track every discovered config path,
	// not just the ones that loaded, so a config file created after startup
	// (e.g. a crushrc added mid-session) is detected as a change.
	store.captureStalenessSnapshot(append(slices.Clone(configPaths), loadedPaths...))

	return store, nil
}

// mustMarshalConfig marshals the config to JSON bytes, returning empty JSON on
// error.
func mustMarshalConfig(cfg *Config) []byte {
	data, err := json.Marshal(cfg)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func PushPopCrushEnv() func() {
	var found []string
	for _, ev := range os.Environ() {
		if strings.HasPrefix(ev, "CRUSH_") {
			pair := strings.SplitN(ev, "=", 2)
			if len(pair) != 2 {
				continue
			}
			found = append(found, strings.TrimPrefix(pair[0], "CRUSH_"))
		}
	}
	backups := make(map[string]string)
	for _, ev := range found {
		backups[ev] = os.Getenv(ev)
	}

	for _, ev := range found {
		os.Setenv(ev, os.Getenv("CRUSH_"+ev))
	}

	restore := func() {
		for k, v := range backups {
			os.Setenv(k, v)
		}
	}
	return restore
}

func (c *Config) configureProviders(ctx context.Context, store *ConfigStore, env env.Env, resolver VariableResolver, knownProviders []catwalk.Provider) error {
	// Crush is fixed to the built-in local Ollama provider. User-defined
	// providers and the external provider catalog are ignored entirely.
	p := builtinOllamaProvider()
	c.Providers.Reset(map[string]ProviderConfig{
		OllamaProviderID: {
			ID:      OllamaProviderID,
			Name:    p.Name,
			BaseURL: p.APIEndpoint,
			Type:    catwalk.Type(OllamaProviderID),
			Models:  p.Models,
		},
	})
	return nil
}

// applyEnv sets top-level env vars from the config. Keys are sorted for
// deterministic ordering so that vars referencing other vars via the
// value resolver produce consistent results.
func (c *Config) applyEnv(resolver VariableResolver) {
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		resolved, err := resolver.ResolveValue(c.Env[k])
		if err != nil {
			slog.Warn("Skipping env var due to resolution failure.", "key", k, "value", c.Env[k], "error", err)
			continue
		}
		os.Setenv(k, resolved)
	}
}

// NormalizeOptions allocates Options and Options.TUI and fills in the option
// defaults the UI relies on, so readers can dereference them without guarding.
// Configs loaded from disk get this via setDefaults; configs arriving over the
// wire from a Crush server need the same treatment before the UI reads them.
//
// DiffMode is deliberately left alone: the permissions dialog reads its zero
// value as "choose split or unified from the terminal width".
func (c *Config) NormalizeOptions() {
	if c.Options == nil {
		c.Options = &Options{}
	}
	if c.Options.TUI == nil {
		c.Options.TUI = &TUIOptions{}
	}
	if c.Options.TUI.Scrollbar == "" {
		c.Options.TUI.Scrollbar = ScrollbarDefault
	}
	if c.Options.TUI.ExitBanner == "" {
		c.Options.TUI.ExitBanner = ExitBannerDefault
	}
}

func (c *Config) setDefaults(workingDir, dataDir string) {
	c.NormalizeOptions()
	if len(c.Options.GlobalContextPaths) == 0 {
		crushConfigDir := filepath.Dir(GlobalConfig())
		c.Options.GlobalContextPaths = []string{
			filepath.Join(crushConfigDir, "CRUSH.md"),
			filepath.Join(filepath.Dir(crushConfigDir), "AGENTS.md"),
		}
	}
	slices.Sort(c.Options.GlobalContextPaths)
	c.Options.GlobalContextPaths = slices.Compact(c.Options.GlobalContextPaths)

	if dataDir != "" {
		c.Options.DataDirectory = dataDir
	} else if c.Options.DataDirectory == "" {
		if path, ok := fsext.LookupClosestBounded(workingDir, projectBoundary(workingDir), defaultDataDirectory); ok {
			c.Options.DataDirectory = path
		} else {
			c.Options.DataDirectory = filepath.Join(workingDir, defaultDataDirectory)
		}
	}
	c.Options.DataDirectory = filepath.Clean(filepathext.SmartJoin(workingDir, c.Options.DataDirectory))
	if c.Providers == nil {
		c.Providers = csync.NewMap[string, ProviderConfig]()
	}
	if c.Models == nil {
		c.Models = make(map[SelectedModelType]SelectedModel)
	}
	if c.RecentModels == nil {
		c.RecentModels = make(map[SelectedModelType][]SelectedModel)
	}
	if c.MCP == nil {
		c.MCP = make(map[string]MCPConfig)
	}
	// Drop orphaned OAuth token entries left behind when a user removes
	// an MCP from crush.json. See MCPConfig.isOrphanedToken.
	for name, m := range c.MCP {
		if m.isOrphanedToken() {
			delete(c.MCP, name)
		}
	}
	if c.LSP == nil {
		c.LSP = make(map[string]LSPConfig)
	}

	// Apply defaults to LSP configurations
	c.applyLSPDefaults()

	// Add the default context paths if they are not already present
	c.Options.ContextPaths = append(slices.Clone(defaultContextPaths), c.Options.ContextPaths...)

	slices.Sort(c.Options.ContextPaths)
	c.Options.ContextPaths = slices.Compact(c.Options.ContextPaths)

	// Add the default skills directories if not already present.
	for _, dir := range GlobalSkillsDirs() {
		if !slices.Contains(c.Options.SkillsPaths, dir) {
			c.Options.SkillsPaths = append(c.Options.SkillsPaths, dir)
		}
	}

	// Project specific skills dirs.
	c.Options.SkillsPaths = append(c.Options.SkillsPaths, ProjectSkillsDir(workingDir)...)

	if c.Options.Attribution == nil {
		c.Options.Attribution = &Attribution{
			TrailerStyle:  TrailerStyleAssistedBy,
			GeneratedWith: true,
		}
	} else if c.Options.Attribution.TrailerStyle == "" {
		// Migrate deprecated co_authored_by or apply default
		if c.Options.Attribution.CoAuthoredBy != nil {
			if *c.Options.Attribution.CoAuthoredBy {
				c.Options.Attribution.TrailerStyle = TrailerStyleCoAuthoredBy
			} else {
				c.Options.Attribution.TrailerStyle = TrailerStyleNone
			}
		} else {
			c.Options.Attribution.TrailerStyle = TrailerStyleAssistedBy
		}
	}

	c.Options.InitializeAs = cmp.Or(c.Options.InitializeAs, defaultInitializeAs)
}

// powernapDefaults caches the powernap default LSP server catalog. The
// catalog is static and immutable for the life of the process, but
// building it (NewManager + LoadDefaults) is expensive and was previously
// repeated on every config reload. We load it once and only ever read from
// it via GetServer, so a shared instance is safe.
var (
	powernapDefaultsOnce sync.Once
	powernapDefaults     *powernapConfig.Manager
)

func lspDefaultsManager() *powernapConfig.Manager {
	powernapDefaultsOnce.Do(func() {
		m := powernapConfig.NewManager()
		// LoadDefaults only fails on malformed embedded defaults, which
		// would be a build-time bug; treat the manager as usable either
		// way so a transient error never wedges config loading.
		_ = m.LoadDefaults()
		powernapDefaults = m
	})
	return powernapDefaults
}

// applyLSPDefaults applies default values from powernap to LSP configurations
func (c *Config) applyLSPDefaults() {
	// Reuse the process-wide default catalog; building it per reload was a
	// significant chunk of reload latency.
	configManager := lspDefaultsManager()

	// Apply defaults to each LSP configuration
	for name, cfg := range c.LSP {
		// Try to get defaults from powernap based on name or command name.
		base, ok := configManager.GetServer(name)
		if !ok {
			base, ok = configManager.GetServer(cfg.Command)
			if !ok {
				continue
			}
		}
		if cfg.Options == nil {
			cfg.Options = base.Settings
		}
		if cfg.InitOptions == nil {
			cfg.InitOptions = base.InitOptions
		}
		if len(cfg.FileTypes) == 0 {
			cfg.FileTypes = base.FileTypes
		}
		if len(cfg.RootMarkers) == 0 {
			cfg.RootMarkers = base.RootMarkers
		}
		cfg.Command = cmp.Or(cfg.Command, base.Command)
		if len(cfg.Args) == 0 {
			cfg.Args = base.Args
		}
		if len(cfg.Env) == 0 {
			cfg.Env = base.Environment
		}
		// Update the config in the map
		c.LSP[name] = cfg
	}
}

func (c *Config) defaultModelSelection(knownProviders []catwalk.Provider) (largeModel SelectedModel, smallModel SelectedModel, err error) {
	largeModel = SelectedModel{
		Provider:  OllamaProviderID,
		Model:     OllamaLargeModelID,
		MaxTokens: 4096,
	}
	smallModel = SelectedModel{
		Provider:  OllamaProviderID,
		Model:     OllamaSmallModelID,
		MaxTokens: 4096,
	}
	return largeModel, smallModel, nil
}

// resolvedModels holds the result of resolving user-configured model
// selections against the provider catalog.
type resolvedModels struct {
	Large         SelectedModel
	Small         SelectedModel
	LargeFallback bool // true if Large was corrected to a default
	SmallFallback bool // true if Small was corrected to a default
}

// resolveSelectedModels validates the user's configured model selections
// against the provider catalog, falling back to defaults when a model ID is
// invalid. It is pure resolution logic: it does not mutate the store or
// touch disk. The caller assigns the results to c.Models and persists any
// fallback corrections as appropriate.
func resolveSelectedModels(cfg *Config, knownProviders []catwalk.Provider) (resolvedModels, error) {
	var result resolvedModels
	defaultLarge, defaultSmall, err := cfg.defaultModelSelection(knownProviders)
	if err != nil {
		return result, fmt.Errorf("failed to select default models: %w", err)
	}
	large, small := defaultLarge, defaultSmall

	if selected, ok := cfg.Models[SelectedModelTypeLarge]; ok {
		large, result.LargeFallback = applySelection(cfg, defaultLarge, selected)
	}
	if selected, ok := cfg.Models[SelectedModelTypeSmall]; ok {
		small, result.SmallFallback = applySelection(cfg, defaultSmall, selected)
	}

	result.Large = large
	result.Small = small
	return result, nil
}

// applySelection overlays a configured selection onto def. It reports a
// fallback, returning def unchanged, when the selection names no known model.
func applySelection(cfg *Config, def, sel SelectedModel) (SelectedModel, bool) {
	out := def
	out.Model = cmp.Or(sel.Model, def.Model)
	out.Provider = cmp.Or(sel.Provider, def.Provider)
	model := cfg.GetModel(out.Provider, out.Model)
	if model == nil {
		return def, true
	}

	out.MaxTokens = model.DefaultMaxTokens
	if sel.MaxTokens > 0 {
		out.MaxTokens = sel.MaxTokens
	}

	// A selection layered over another model's can hold reasoning options
	// this one rejects, so they only apply to a model that reasons.
	out.ReasoningEffort, out.Think = "", false
	if model.CanReason() {
		out.ReasoningEffort = cmp.Or(sel.ReasoningEffort, model.Reasoning.DefaultEffortLevel)
		out.Think = sel.Think
	} else if sel.Think || sel.ReasoningEffort != "" {
		slog.Warn(
			"Ignoring reasoning options for a model that does not declare reasoning support",
			"provider", out.Provider,
			"model", out.Model,
			"hint", `set "reasoning": {"thinking": "toggleable"} on the model if it reasons`,
		)
	}

	out.Temperature = cmp.Or(sel.Temperature, out.Temperature)
	out.TopP = cmp.Or(sel.TopP, out.TopP)
	out.TopK = cmp.Or(sel.TopK, out.TopK)
	out.FrequencyPenalty = cmp.Or(sel.FrequencyPenalty, out.FrequencyPenalty)
	out.PresencePenalty = cmp.Or(sel.PresencePenalty, out.PresencePenalty)
	if sel.ProviderOptions != nil {
		out.ProviderOptions = maps.Clone(sel.ProviderOptions)
	}
	return out, false
}

// lookupConfigs searches config files starting at cwd and walking up
// through the current project. The upward walk stops at the git
// working tree root when one can be detected, otherwise at cwd itself,
// so an unrelated crush.json placed above the project is never picked
// up. Global user-level config locations are always included
// regardless of the boundary.
func lookupConfigs(cwd string) []string {
	// Prepend global user config and machine-owned data JSON. Only the user
	// config directory contributes a crushrc; the data directory is writable
	// machine state and must never be executed as Bash. Missing files are
	// skipped when loaded.
	configPaths := []string{
		systemConfigPath,
		GlobalConfig(),
		shellConfigSibling(GlobalConfig()),
		GlobalConfigData(),
	}

	// Ordered high-to-low priority within a directory. LookupBounded returns
	// matches in this order, and the later reverse + merge make the earliest
	// listed name win on conflict. So: .crushrc beats crushrc, both beat the
	// JSON configs, and .crush.json beats crush.json.
	configNames := []string{
		"." + appName + "rc",
		appName + "rc",
		"." + appName + ".json",
		appName + ".json",
	}

	foundConfigs, err := fsext.LookupBounded(cwd, projectBoundary(cwd), configNames...)
	if err != nil {
		// returns at least default configs
		return configPaths
	}

	// reverse order so last config has more priority
	slices.Reverse(foundConfigs)

	return append(configPaths, foundConfigs...)
}

func loadFromConfigPaths(ctx context.Context, configPaths []string) (*Config, []string, error) {
	var configs [][]byte
	var loaded []string

	// Track directories that have both crush.json and crushrc to warn
	// about potential confusion, along with the top-level keys each
	// defines so we can report conflicts.
	jsonDirKeys := make(map[string]map[string]bool)
	shDirKeys := make(map[string]map[string]bool)

	for _, path := range configPaths {
		if path == "" {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("failed to open config file %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}

		dir := filepath.Dir(path)
		if isShellConfig(path) {
			jsonBytes, err := shellconfig.LoadShellConfig(ctx, path, data)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to load shell config %s: %w", path, err)
			}
			if len(jsonBytes) > 0 {
				if !json.Valid(jsonBytes) {
					return nil, nil, fmt.Errorf("shell config %s produced invalid JSON", path)
				}
				addTopLevelKeys(shDirKeys, dir, jsonBytes)
				configs = append(configs, jsonBytes)
				loaded = append(loaded, path)
			}
		} else {
			if !json.Valid(data) {
				return nil, nil, fmt.Errorf("invalid JSON in config file %s", path)
			}
			data = migrateCatwalkV2File(path, data)
			addTopLevelKeys(jsonDirKeys, dir, data)
			configs = append(configs, data)
			loaded = append(loaded, path)
		}
	}

	// Warn if both a JSON config and a crushrc exist in the same directory
	// and define overlapping top-level keys. Disjoint coexistence is
	// intentional and not worth warning about.
	for dir, jKeys := range jsonDirKeys {
		sKeys, ok := shDirKeys[dir]
		if !ok {
			continue
		}
		var conflicts []string
		for k := range jKeys {
			if sKeys[k] {
				conflicts = append(conflicts, k)
			}
		}
		if len(conflicts) > 0 {
			slices.Sort(conflicts)
			slog.Warn("Found both a JSON config and a crushrc in the same directory; merging with crushrc taking precedence",
				"dir", dir, "conflicting_keys", strings.Join(conflicts, ", "))
		}
	}

	cfg, err := loadFromBytes(configs)
	if err != nil {
		return nil, nil, err
	}
	return cfg, loaded, nil
}

// addTopLevelKeys records the top-level JSON keys present in data into the
// set for dir.
func addTopLevelKeys(m map[string]map[string]bool, dir string, data []byte) {
	keys := m[dir]
	if keys == nil {
		keys = make(map[string]bool)
		m[dir] = keys
	}
	gjson.ParseBytes(data).ForEach(func(key, _ gjson.Result) bool {
		keys[key.String()] = true
		return true
	})
}

func loadFromBytes(configs [][]byte) (*Config, error) {
	if len(configs) == 0 {
		return &Config{}, nil
	}

	data, err := jsons.Merge(configs)
	if err != nil {
		return nil, err
	}
	// Safety net for sources that cannot be rewritten on disk (e.g.
	// crushrc builtins producing JSON): migrate the merged result in
	// memory so decoding always sees the v3 schema.
	data, _ = migrateCatwalkV2(data)
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// migrateDisableNotifications migrates the deprecated disable_notifications
// and notification_style fields to the unified notifications field. It checks
// both the user config (~/.config) and data config (~/.local) files. If
// disable_notifications is true, it sets notifications to "disabled" in the
// data file. If notification_style is set, it moves the value to notifications.
// Regardless of value, it removes the deprecated fields from any file that
// contains them.
func migrateDisableNotifications() {
	globalConfig := GlobalConfig()
	dataConfig := GlobalConfigData()

	var wasDisabled bool
	var styleValue string
	filesToClean := []string{}

	for _, path := range []string{globalConfig, dataConfig} {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		needsClean := false
		if gjson.Get(string(data), "options.disable_notifications").Exists() {
			needsClean = true
			if gjson.Get(string(data), "options.disable_notifications").Bool() {
				wasDisabled = true
			}
		}
		if v := gjson.Get(string(data), "options.notification_style"); v.Exists() {
			needsClean = true
			if styleValue == "" {
				styleValue = v.String()
			}
		}
		if needsClean {
			filesToClean = append(filesToClean, path)
		}
	}

	if len(filesToClean) == 0 {
		return
	}

	// Determine the value to persist: notification_style takes precedence,
	// then disable_notifications: true maps to "disabled".
	migratedValue := styleValue
	if migratedValue == "" && wasDisabled {
		migratedValue = "disabled"
	}

	if migratedValue != "" {
		data, err := readFile(dataConfig)
		if err == nil {
			if !gjson.Get(string(data), "options.notifications").Exists() {
				updated, err := sjson.Set(string(data), "options.notifications", migratedValue)
				if err == nil {
					if err := atomicWriteFile(dataConfig, []byte(updated), 0o600); err != nil {
						slog.Warn("Failed to migrate to notifications field", "error", err)
					} else {
						slog.Info("Migrated notification settings to notifications field", "value", migratedValue)
					}
				}
			}
		}
	}

	// Remove deprecated fields from all files that contain them.
	for _, path := range filesToClean {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		updated := string(data)
		updated, _ = sjson.Delete(updated, "options.disable_notifications")
		updated, _ = sjson.Delete(updated, "options.notification_style")
		if updated == string(data) {
			continue
		}
		if err := atomicWriteFile(path, []byte(updated), 0o600); err != nil {
			slog.Warn("Failed to write migrated config", "path", path, "error", err)
		}
	}
}

// GlobalConfig returns the global configuration file path for the application.
func GlobalConfig() string {
	if crushGlobal := os.Getenv("CRUSH_GLOBAL_CONFIG"); crushGlobal != "" {
		return filepath.Join(crushGlobal, fmt.Sprintf("%s.json", appName))
	}
	return filepath.Join(home.Config(), appName, fmt.Sprintf("%s.json", appName))
}

// shellConfigSibling returns the crushrc path that sits alongside a given
// crush.json path (same directory). Used so global config locations pick up a
// shell config, not just JSON.
func shellConfigSibling(jsonPath string) string {
	return filepath.Join(filepath.Dir(jsonPath), appName+"rc")
}

// isShellConfig reports whether a config path is a shell config (crushrc or
// the hidden .crushrc), as opposed to a JSON config.
func isShellConfig(path string) bool {
	base := filepath.Base(path)
	return base == appName+"rc" || base == "."+appName+"rc"
}

// GlobalCacheDir returns the path to the global cache directory for the
// application.
func GlobalCacheDir() string {
	if crushCache := os.Getenv("CRUSH_CACHE_DIR"); crushCache != "" {
		return crushCache
	}
	if xdgCacheHome := os.Getenv("XDG_CACHE_HOME"); xdgCacheHome != "" {
		return filepath.Join(xdgCacheHome, appName)
	}
	if runtime.GOOS == "windows" {
		localAppData := cmp.Or(
			os.Getenv("LOCALAPPDATA"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
		)
		return filepath.Join(localAppData, appName, "cache")
	}
	return filepath.Join(home.Dir(), ".cache", appName)
}

// ProjectConfigs returns list of current project configs paths.
func ProjectConfigs(cwd string) []string {
	return lookupConfigs(cwd)
}

// GlobalConfigData returns the path to the main data directory for the application.
// this config is used when the app overrides configurations instead of updating the global config.
func GlobalConfigData() string {
	if crushData := os.Getenv("CRUSH_GLOBAL_DATA"); crushData != "" {
		return filepath.Join(crushData, fmt.Sprintf("%s.json", appName))
	}
	if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
		return filepath.Join(xdgDataHome, appName, fmt.Sprintf("%s.json", appName))
	}

	// return the path to the main data directory
	// for windows, it should be in `%LOCALAPPDATA%/crush/`
	// for linux and macOS, it should be in `$HOME/.local/share/crush/`
	if runtime.GOOS == "windows" {
		localAppData := cmp.Or(
			os.Getenv("LOCALAPPDATA"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
		)
		return filepath.Join(localAppData, appName, fmt.Sprintf("%s.json", appName))
	}

	return filepath.Join(home.Dir(), ".local", "share", appName, fmt.Sprintf("%s.json", appName))
}

// GlobalWorkspaceDir returns the path to the global server workspace
// directory. This directory acts as a meta-workspace for the server
// process, giving it a real workingDir so that config loading, scoped
// writes, and provider resolution behave identically to project
// workspaces.
func GlobalWorkspaceDir() string {
	return filepath.Dir(GlobalConfigData())
}

func assignIfNil[T any](ptr **T, val T) {
	if *ptr == nil {
		*ptr = &val
	}
}

// isInsideWorktree reports whether dir is inside a git working tree. It
// checks dir rather than the process working directory, which a server
// shares across every workspace it hosts.
func isInsideWorktree(dir string) bool {
	cmd := exec.CommandContext(
		context.Background(),
		"git", "rev-parse",
		"--is-inside-work-tree",
	)
	cmd.Dir = dir
	bts, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(bts)) == "true"
}

// worktreeRoot returns the absolute path of the git working tree root for
// dir, or the empty string if dir is not inside a working tree (bare
// repositories, missing git binary, plain directories, or any other
// failure mode). Linked worktrees and submodules each report their own
// top-level, which is what callers want when bounding lookups.
// worktreeRootCache memoizes the git worktree root per directory. The root
// is stable for the life of the process, so we avoid re-shelling out to
// "git rev-parse" on every config reload. Keyed by the requested dir; the
// value is the resolved root ("" when dir is not in a git worktree).
var worktreeRootCache sync.Map // map[string]string

func worktreeRoot(dir string) string {
	if cached, ok := worktreeRootCache.Load(dir); ok {
		return cached.(string)
	}
	root := computeWorktreeRoot(dir)
	worktreeRootCache.Store(dir, root)
	return root
}

func computeWorktreeRoot(dir string) string {
	cmd := exec.CommandContext(
		context.Background(),
		"git", "rev-parse", "--show-toplevel",
	)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return abs
}

// projectBoundary returns the directory at which an upward configuration
// search rooted at dir should stop. It is the git working tree root when
// one can be detected, otherwise dir itself. Returning dir as a
// fallback keeps Crush from silently adopting state files placed above
// the current project.
func projectBoundary(dir string) string {
	if root := worktreeRoot(dir); root != "" {
		return root
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// GlobalSkillsDirs returns the default directories for Agent Skills.
// Skills in these directories are auto-discovered and their files can be read
// without permission prompts.
func GlobalSkillsDirs() []string {
	if crushSkills := os.Getenv("CRUSH_SKILLS_DIR"); crushSkills != "" {
		return []string{crushSkills}
	}

	paths := []string{
		filepath.Join(home.Config(), appName, "skills"),
		filepath.Join(home.Config(), "agents", "skills"),
		// Per the Agent Skills spec, scan ~/.agents/skills
		filepath.Join(home.Dir(), ".agents", "skills"),
		filepath.Join(home.Dir(), ".claude", "skills"),
	}

	// On Windows, also load from app data on top of `$HOME/.config/crush`.
	// This is here mostly for backwards compatibility.
	if runtime.GOOS == "windows" {
		appData := cmp.Or(
			os.Getenv("LOCALAPPDATA"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
		)
		paths = append(
			paths,
			filepath.Join(appData, appName, "skills"),
			filepath.Join(appData, "agents", "skills"),
		)
	}

	return paths
}

// projectSkillSubdirs lists the conventional subdirectories where
// project-level skills are discovered. Shared across working-dir and
// git-root lookups to prevent drift when a new convention is added.
var projectSkillSubdirs = []string{
	".agents/skills",
	".crush/skills",
	".claude/skills",
	".cursor/skills",
}

// ProjectSkillsDir returns the default project directories for which Crush
// will look for skills. In addition to the working directory, it also
// checks the git working tree root so that monorepo-level skills are
// discovered when the user is inside a subdirectory.
// Working-directory paths come first so local skills take precedence
// over monorepo-level ones.
func ProjectSkillsDir(workingDir string) []string {
	dirs := make([]string, 0, len(projectSkillSubdirs)*2)
	for _, sub := range projectSkillSubdirs {
		dirs = append(dirs, filepath.Join(workingDir, sub))
	}

	// When the working directory is inside a git repository, also look at
	// the repository root so monorepo-level .agents/skills are found.
	if root := worktreeRoot(workingDir); root != "" && root != workingDir {
		for _, sub := range projectSkillSubdirs {
			dirs = append(dirs, filepath.Join(root, sub))
		}
	}

	return dirs
}

func isAppleTerminal() bool { return os.Getenv("TERM_PROGRAM") == "Apple_Terminal" }
