package agent

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"golang.org/x/sync/errgroup"

	"charm.land/fantasy/providers/openaicompat"
	openaisdk "github.com/charmbracelet/openai-go/option"
	"github.com/qjebbs/go-jsons"
)

// Coordinator errors.
var (
	errCoderAgentNotConfigured    = errors.New("coder agent not configured")
	errPlanAgentNotConfigured     = errors.New("plan agent not configured")
	errMainAgentNotFound          = errors.New("main agent not found")
	errModelProviderNotConfigured = errors.New("model provider not configured")
	errModelNotSelected           = errors.New("model not selected")
	errModelNotFound              = errors.New("model not found in provider config")
)

// servingProvider returns the Fantasy provider package that serves the
// built-in Ollama provider. Ollama speaks the OpenAI chat-completions
// protocol, so every request goes through the OpenAI-compatible client.
func servingProvider(_ config.ProviderConfig, _ catwalk.Model) string {
	return openaicompat.Name
}

type Coordinator interface {
	SetMainAgent(agentName string) error
	Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	// RunAccepted runs a call that was already accepted via
	// BeginAccepted on the fire-and-forget dispatch path. The handle is
	// the only carrier of accept-state across the backend.runAgent /
	// Coordinator / sessionAgent.Run layers: it reaches
	// sessionAgent.Run as SessionAgentCall.Accepted, where it is
	// consumed under dispatchMu once the accepted -> (cancel-on-entry |
	// queued | active) transition is chosen.
	RunAccepted(ctx context.Context, accept *AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	BeginAccepted(sessionID string) *AcceptedRun
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	// ListSettledMessages reads a session's transcript, first settling any
	// tool calls a dead process left without a reply.
	ListSettledMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	AddNote(sessionID, text string)
	Summarize(context.Context, string) error
	Model() Model
	UpdateModels(ctx context.Context) error
	GenerateTitle(ctx context.Context, sessionID, prompt string)
	// SetHostContext records what the host application has in view;
	// each run from then on tells the model about it.
	SetHostContext(hc proto.HostContext)
	HostContext() proto.HostContext
}

type coordinator struct {
	cfg         *config.ConfigStore
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	questions   question.Service
	history     history.Service
	filetracker filetracker.Service
	lspManager  *lsp.Manager
	notify      pubsub.Publisher[notify.Notification]
	runComplete pubsub.Publisher[notify.RunComplete]
	interactive bool
	shellOwner  any

	// agentMu guards mainAgent and mainAgentName: SetMainAgent runs on
	// HTTP handler goroutines while runs, cancels, and probes read the
	// current agent from their own goroutines.
	agentMu       sync.RWMutex
	mainAgent     SessionAgent
	mainAgentName string
	agents        map[string]SessionAgent
	hookRunner    *hooks.Runner

	// models is shared by every agent; rebuildMu serializes rebuilds of it.
	models    csync.Value[coordinatorModels]
	rebuildMu sync.Mutex

	// modelsGen counts rebuilds; override models rebuild when it moves.
	modelsGen atomic.Uint64
	// overrideModels shares builds of per-dispatch model overrides.
	overrideModelsMu sync.Mutex
	overrideModels   map[overrideModelKey]*overrideModel

	// dispatched tracks detached subagents by parent session and label.
	dispatched *csync.Map[string, *dispatchedAgent]
	// dispatchMu makes checking a label and registering it one step.
	dispatchMu sync.Mutex
	// names hands each new subagent its first name.
	names subagentNamer
	// backgroundAfter is how long a blocking subagent runs before it is
	// moved to the background. It mirrors the bash tool's threshold and is
	// a field only so tests can shorten it.
	backgroundAfter time.Duration
	// reconciled records parent sessions already checked for subagents a
	// previous process left unreported. Keys are session IDs.
	reconciled sync.Map
	// subSessions counts the runs in flight on each subagent session, so
	// the busy check sees subagents the main agent is not running itself.
	subSessionsMu sync.Mutex
	subSessions   map[string]int
	// settleMu serializes transcript repairs so two readers cannot both
	// settle one orphaned call.
	settleMu sync.Mutex

	// Skills discovery results (session-start snapshot).
	allSkills    []*skills.Skill // Pre-filter: all discovered after dedup.
	activeSkills []*skills.Skill // Post-filter: active skills only.
	skillTracker *skills.Tracker

	// prompts holds each main agent's prompt template, and skillKeys the
	// skill toggle state its system prompt was last built with, so a
	// toggle rebuilds the prompt on the next turn. Guarded by skillKeysMu.
	prompts     map[string]*prompt.Prompt
	skillKeys   map[string]string
	skillKeysMu sync.Mutex

	readyWg errgroup.Group

	hostContext csync.Value[proto.HostContext]

	// titles is shared by every agent built here, sub-agents included, so
	// CancelAll on the current agent drains all title generation.
	titles titleTracker
}

// CoordinatorOptions holds the dependencies for NewCoordinator. Using a
// struct keeps the constructor self-documenting and avoids a long
// positional parameter list.
type CoordinatorOptions struct {
	Config      *config.ConfigStore
	Sessions    session.Service
	Messages    message.Service
	Permissions permission.Service
	Questions   question.Service
	History     history.Service
	FileTracker filetracker.Service
	LSPManager  *lsp.Manager
	Notify      pubsub.Publisher[notify.Notification]
	RunComplete pubsub.Publisher[notify.RunComplete]
	Skills      *skills.Manager
	HookRunner  *hooks.Runner
	Interactive bool
	// ShellOwner tags the background shells the bash tool starts, so the
	// owner can stop its own shells without touching anyone else's.
	ShellOwner any
}

func NewCoordinator(ctx context.Context, opts CoordinatorOptions) (Coordinator, error) {
	// Skills are pre-discovered by the caller (see app.New /
	// backend.CreateWorkspace) and passed in via the manager. If no
	// manager was provided (legacy callers), fall back to an in-line
	// discovery so the coordinator still works.
	var allSkills, activeSkills []*skills.Skill
	if opts.Skills != nil {
		allSkills = opts.Skills.AllSkills()
		activeSkills = opts.Skills.ActiveSkills()
	} else {
		allSkills, activeSkills = discoverSkills(opts.Config)
	}
	skillTracker := skills.NewTracker(activeSkills)

	c := &coordinator{
		cfg:             opts.Config,
		sessions:        opts.Sessions,
		messages:        opts.Messages,
		permissions:     opts.Permissions,
		questions:       opts.Questions,
		history:         opts.History,
		filetracker:     opts.FileTracker,
		lspManager:      opts.LSPManager,
		notify:          opts.Notify,
		runComplete:     opts.RunComplete,
		agents:          make(map[string]SessionAgent),
		allSkills:       allSkills,
		activeSkills:    activeSkills,
		skillTracker:    skillTracker,
		interactive:     opts.Interactive,
		hookRunner:      opts.HookRunner,
		shellOwner:      opts.ShellOwner,
		dispatched:      csync.NewMap[string, *dispatchedAgent](),
		backgroundAfter: subAgentBackgroundAfter,
	}

	agentCfg, ok := opts.Config.Config().Agents[config.AgentCoder]
	if !ok {
		return nil, errCoderAgentNotConfigured
	}

	coderPrompt, err := coderPrompt(c.skillOverrideOptions()...)
	if err != nil {
		return nil, err
	}

	agent, err := c.buildAgent(ctx, &c.readyWg, coderPrompt, agentCfg, false)
	if err != nil {
		return nil, err
	}
	c.agents[config.AgentCoder] = agent

	planCfg, ok := c.cfg.Config().Agents[config.AgentPlan]
	if !ok {
		return nil, errPlanAgentNotConfigured
	}

	planSystemPrompt, err := planPrompt(append(
		c.skillOverrideOptions(),
		prompt.WithToolNames(c.effectiveToolNames(planCfg, false)),
	)...)
	if err != nil {
		return nil, err
	}

	planAgent, err := c.buildAgent(ctx, &c.readyWg, planSystemPrompt, planCfg, false)
	if err != nil {
		return nil, err
	}
	c.agents[config.AgentPlan] = planAgent

	c.prompts = map[string]*prompt.Prompt{
		config.AgentCoder: coderPrompt,
		config.AgentPlan:  planSystemPrompt,
	}
	key := c.skillToggleKey(ctx)
	c.skillKeys = map[string]string{
		config.AgentCoder: key,
		config.AgentPlan:  key,
	}

	c.mainAgent = agent
	c.mainAgentName = config.AgentCoder
	return c, nil
}

// activeAgent returns the coordinator's current main agent and its config
// name as one snapshot. Callers use the snapshot for the whole operation,
// so a SetMainAgent racing mid-flight never splits a run, model refresh, or
// summarize across two agents.
func (c *coordinator) activeAgent() (SessionAgent, string) {
	c.agentMu.RLock()
	defer c.agentMu.RUnlock()
	return c.mainAgent, c.mainAgentName
}

// currentAgent returns the current main agent.
func (c *coordinator) currentAgent() SessionAgent {
	agent, _ := c.activeAgent()
	return agent
}

func (c *coordinator) SetMainAgent(agentName string) error {
	c.agentMu.Lock()
	defer c.agentMu.Unlock()
	agent, ok := c.agents[agentName]
	if !ok {
		return fmt.Errorf("%w: %s", errMainAgentNotFound, agentName)
	}
	c.mainAgent = agent
	c.mainAgentName = agentName
	return nil
}

// Run implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.run(ctx, nil, nil, sessionID, prompt, attachments...)
}

// RunAccepted implements Coordinator.
func (c *coordinator) RunAccepted(ctx context.Context, accept *AcceptedRun, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.run(ctx, accept, nil, sessionID, prompt, attachments...)
}

// run is the shared implementation behind Run and RunAccepted. When
// accept is non-nil it is threaded onto the SessionAgentCall as
// Accepted so sessionAgent.Run can consume the accept reservation under
// dispatchMu; when nil (the in-process/local path) no accept tracking
// applies.
// runReport delivers a subagent report: it folds into a turn in progress, or
// starts one.
func (c *coordinator) runReport(ctx context.Context, sessionID string, report message.SubAgentReport) (*fantasy.AgentResult, error) {
	return c.run(ctx, nil, &report, sessionID, report.PromptText())
}

func (c *coordinator) run(ctx context.Context, accept *AcceptedRun, report *message.SubAgentReport, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	if err := c.readyWg.Wait(); err != nil {
		return nil, err
	}

	// MCP servers connect asynchronously (see mcp.Initialize).
	//
	// Interactive runs never wait for that to finish: the tool list below
	// is built from whatever is registered right now, servers still
	// connecting are simply absent from this run's palette, and they are
	// picked up by later runs once they register and publish
	// EventToolsListChanged. Blocking here froze the TUI for the duration
	// of the slowest server's connect timeout whenever a prompt was sent
	// before initialization finished — most visibly on the first message.
	//
	// Non-interactive runs get a single shot at the tool palette, so they
	// do wait for initialization to settle — but bounded by InitWaitBudget
	// rather than each server's connect timeout, so a server wedged
	// mid-handshake cannot stall a headless run for minutes. Past the
	// budget the turn proceeds without the stragglers; their tools simply
	// stay absent from this run.
	if !c.interactive {
		if err := mcp.WaitForInitBudget(ctx, mcp.InitWaitBudget); err != nil {
			return nil, fmt.Errorf("failed to wait for MCP initialization: %w", err)
		}
	}

	// refresh models before each run. Snapshot the agent first: the run,
	// its model settings, and the model refresh below must all target the
	// same agent even if SetMainAgent swaps the main agent mid-flight.
	agent, agentName := c.activeAgent()
	if err := c.updateAgentModels(ctx, agent, agentName); err != nil {
		return nil, fmt.Errorf("failed to update models: %w", err)
	}
	if err := c.refreshSkillPrompt(ctx, agent, agentName); err != nil {
		slog.Error("Failed to rebuild system prompt after skill toggle", "error", err)
	}

	model := agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)

	// Inject session_id for OpenRouter sticky routing (no-op for other providers).

	// Coalesce per-attempt RunComplete payloads so only the final
	// outcome reaches subscribers. Without this, the first attempt's
	// failed RunComplete (unauthorized) would race ahead of the
	// retry's success, and `crush run` would exit on the stale error
	// before ever seeing the retry result. Each attempt's
	// SessionAgentCall.OnComplete hook overwrites latest; we publish
	// exactly once after retries resolve, via PublishMustDeliver, so
	// a momentarily-full subscriber buffer can't silently drop the
	// terminal event.
	var (
		latest    notify.RunComplete
		hasLatest bool
	)
	onComplete := func(rc notify.RunComplete) {
		latest = rc
		hasLatest = true
	}
	// Before the first turn on a session, tell it about any subagent a
	// previous process left without a report.
	c.reconcileDispatches(ctx, agent, sessionID)

	// Propagate the caller-supplied RunID (set via agent.WithRunID
	// at the HTTP boundary in backend.SendMessage) onto the
	// SessionAgentCall so the terminal RunComplete event echoes it
	// back. Both attempts in the retry chain reuse the same RunID;
	// the coalesce closure publishes the final outcome under that
	// same correlator.
	runID := RunIDFromContext(ctx)
	channel := ChannelFromContext(ctx)
	c.syncSessionChannel(ctx, sessionID, channel)
	run := func() (*fantasy.AgentResult, error) {
		return agent.Run(ctx, SessionAgentCall{
			SessionID:         sessionID,
			RunID:             runID,
			Channel:           channel,
			Prompt:            prompt,
			HostContext:       c.hostContext.Get(),
			Report:            report,
			HiddenUserMessage: message.HiddenUserMessage(ctx),
			Attachments:       attachments,
			MaxOutputTokens:   maxTokens,
			ProviderOptions:   mergedOptions,
			Temperature:       temp,
			TopP:              topP,
			TopK:              callTopK(providerCfg, topK),
			FrequencyPenalty:  freqPenalty,
			PresencePenalty:   presPenalty,
			OnComplete:        onComplete,
			Accepted:          accept,
		})
	}
	beforeLoaded := c.skillTracker.LoadedNames()
	result, originalErr := run()
	logTurnSkillUsage(sessionID, prompt, c.activeSkills, c.skillTracker, beforeLoaded)

	if hasLatest && c.runComplete != nil {
		c.runComplete.PublishMustDeliver(ctx, pubsub.UpdatedEvent, latest)
		// Signal to the dispatcher (backend.runAgent) that the
		// authoritative terminal RunComplete for this run was already
		// emitted, so it does not publish a duplicate fallback for the
		// error it is about to receive.
		MarkRunCompletePublished(ctx)
	}
	return result, originalErr
}

// syncSessionChannel reconciles the session's persisted channel binding with
// the origin of the turn about to run. A channel-originated turn (re)binds
// the session to that channel — the newest push wins — and a local turn
// clears a stale binding, since the session is no longer channel-driven once
// the user takes it over directly. This is the binding's whole lifecycle:
// it is only ever a reflection of the most recent turn's origin, and the
// column is dropped with the session row when the session is deleted, so
// there is no separate state to reap.
//
// Failures are logged and the turn proceeds: the binding is provenance for
// reply routing, not a precondition for running.
func (c *coordinator) syncSessionChannel(ctx context.Context, sessionID, channel string) {
	sess, err := c.sessions.Get(ctx, sessionID)
	if err != nil {
		// A missing session is expected (it may be created later in the
		// run), but a real database failure would otherwise be silent.
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("Failed to load session for channel binding sync",
				"session", sessionID, "channel", channel, "error", err)
		}
		return
	}
	if sess.Channel == channel {
		return
	}
	if _, err := c.sessions.SetChannel(ctx, sessionID, channel); err != nil {
		slog.Warn("Failed to sync session channel binding",
			"session", sessionID, "channel", channel, "error", err)
	}
}

// effectiveReasoningEffort returns the reasoning effort to apply for provider calls.
// It prefers the user-selected effort when valid, otherwise the model default when
// valid, and finally falls back to the first configured reasoning level.
func effectiveReasoningEffort(model Model) string {
	if !model.CatwalkCfg.CanReason() {
		return ""
	}

	levels := model.CatwalkCfg.ReasoningEffortLevels()
	if effort := model.ModelCfg.ReasoningEffort; effort != "" && slices.Contains(levels, effort) {
		return effort
	}
	if effort := model.CatwalkCfg.Reasoning.DefaultEffortLevel; effort != "" && slices.Contains(levels, effort) {
		return effort
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

func getProviderOptions(model Model, providerCfg config.ProviderConfig) fantasy.ProviderOptions {
	options := fantasy.ProviderOptions{}

	cfgOpts := []byte("{}")
	providerCfgOpts := []byte("{}")
	catwalkOpts := []byte("{}")

	if model.ModelCfg.ProviderOptions != nil {
		data, err := json.Marshal(model.ModelCfg.ProviderOptions)
		if err == nil {
			cfgOpts = data
		}
	}

	if providerCfg.ProviderOptions != nil {
		data, err := json.Marshal(providerCfg.ProviderOptions)
		if err == nil {
			providerCfgOpts = data
		}
	}

	if model.CatwalkCfg.Options.ProviderOptions != nil {
		data, err := json.Marshal(model.CatwalkCfg.Options.ProviderOptions)
		if err == nil {
			catwalkOpts = data
		}
	}

	readers := []io.Reader{
		bytes.NewReader(catwalkOpts),
		bytes.NewReader(providerCfgOpts),
		bytes.NewReader(cfgOpts),
	}

	got, err := jsons.Merge(readers)
	if err != nil {
		slog.Error("Could not merge call config", "err", err)
		return options
	}

	mergedOptions := make(map[string]any)

	err = json.Unmarshal([]byte(got), &mergedOptions)
	if err != nil {
		slog.Error("Could not create config for call", "err", err)
		return options
	}

	reasoningEffort := effectiveReasoningEffort(model)
	levels := model.CatwalkCfg.ReasoningEffortLevels()
	shouldSetEffort := model.CatwalkCfg.CanReason() &&
		reasoningEffort != "" &&
		slices.Contains(levels, reasoningEffort)

	// Ollama speaks OpenAI-compatible chat completions. top_k is not part
	// of the OpenAI protocol, so it is carried in extra_body instead of on
	// the call itself (see callTopK).
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	extraBody, hasExtraBody := mergedOptions["extra_body"].(map[string]any)
	if !hasExtraBody {
		extraBody = make(map[string]any)
		mergedOptions["extra_body"] = extraBody
	}
	if topK != nil {
		if _, hasTopK := extraBody["top_k"]; !hasTopK {
			extraBody["top_k"] = *topK
		}
	}

	_, hasReasoningEffort := mergedOptions["reasoning_effort"]
	if !hasReasoningEffort && shouldSetEffort {
		mergedOptions["reasoning_effort"] = reasoningEffort
	}

	parsed, err := openaicompat.ParseOptions(mergedOptions)
	if err == nil {
		options[openaicompat.Name] = parsed
	} else if topK != nil {
		slog.Warn(
			"Failed to parse provider_options, falling back to top_k only",
			"provider", providerCfg.ID,
			"error", err,
		)
		fallback, fallbackErr := openaicompat.ParseOptions(map[string]any{
			"extra_body": map[string]any{"top_k": *topK},
		})
		if fallbackErr == nil {
			options[openaicompat.Name] = fallback
		} else {
			slog.Warn(
				"Failed to parse fallback provider options, this should never happen",
				"provider", providerCfg.ID,
				"error", fallbackErr,
			)
		}
	}

	return options
}

func mergeCallOptions(model Model, cfg config.ProviderConfig) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(model, cfg)
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

// skillOverrideOptions returns the prompt options carrying the
// repository-scoped skill toggle overrides. They read the DB at Build
// time, the same point where the config's disabled list is applied;
// refreshSkillPrompt rebuilds on the next turn after a toggle.
func (c *coordinator) skillOverrideOptions() []prompt.Option {
	disabled := func(ctx context.Context) []string {
		names, err := c.sessions.SkillsDisabled(ctx)
		if err != nil {
			slog.Error("Failed to list disabled skills for prompt", "error", err)
			return nil
		}
		return names
	}
	enabled := func(ctx context.Context) []string {
		names, err := c.sessions.SkillsEnabled(ctx)
		if err != nil {
			slog.Error("Failed to list enabled skills for prompt", "error", err)
			return nil
		}
		return names
	}
	return []prompt.Option{
		prompt.WithWorkingDir(c.cfg.WorkingDir()),
		prompt.WithSkillOverrides(disabled, enabled),
	}
}

// skillToggleKey fingerprints everything that decides which skills the
// system prompt lists: the config's disabled list and the repository
// overrides.
func (c *coordinator) skillToggleKey(ctx context.Context) string {
	var configDisabled []string
	if opts := c.cfg.Config().Options; opts != nil {
		configDisabled = opts.DisabledSkills
	}
	repoDisabled, _ := c.sessions.SkillsDisabled(ctx)
	repoEnabled, _ := c.sessions.SkillsEnabled(ctx)
	return fmt.Sprint(configDisabled, repoDisabled, repoEnabled)
}

// refreshSkillPrompt rebuilds the agent's system prompt when the skill
// toggles changed since it was last built, so a toggle applies from the
// next turn. Otherwise the prompt is left alone, keeping the provider's
// prompt cache warm.
func (c *coordinator) refreshSkillPrompt(ctx context.Context, agent SessionAgent, name string) error {
	p, ok := c.prompts[name]
	if !ok {
		return nil
	}
	key := c.skillToggleKey(ctx)

	c.skillKeysMu.Lock()
	defer c.skillKeysMu.Unlock()
	if c.skillKeys[name] == key {
		return nil
	}
	model := agent.Model().Model
	systemPrompt, err := p.Build(ctx, model.Provider(), model.Model(), c.cfg)
	if err != nil {
		return err
	}
	agent.SetSystemPrompt(systemPrompt)
	c.skillKeys[name] = key
	return nil
}

func (c *coordinator) buildAgent(ctx context.Context, ready *errgroup.Group, prompt *prompt.Prompt, agent config.Agent, isSubAgent bool) (SessionAgent, error) {
	return c.buildAgentWithModels(ctx, ready, prompt, agent, isSubAgent, nil)
}

// buildAgentWithModels builds an agent whose model set can be replaced.
// modelsFn nil keeps the coordinator's shared set: main models for the
// coder and plan agents, subagent models for subagents. A dispatch with
// a model override passes a function serving that override, so every
// per-turn model read picks it up.
func (c *coordinator) buildAgentWithModels(ctx context.Context, ready *errgroup.Group, prompt *prompt.Prompt, agent config.Agent, isSubAgent bool, modelsFn func() ModelSet) (SessionAgent, error) {
	if c.models.Get().large.Model == nil {
		if err := c.rebuildModels(ctx); err != nil {
			return nil, err
		}
	}
	models := c.mainModels
	if isSubAgent {
		models = c.subAgentModels
	}
	if modelsFn != nil {
		models = modelsFn
	}
	large := models().Large

	largeProviderCfg, _ := c.cfg.Config().Providers.Get(large.ModelCfg.Provider)
	result := NewSessionAgent(SessionAgentOptions{
		Models:               models,
		SystemPromptPrefix:   largeProviderCfg.SystemPromptPrefix,
		SystemPrompt:         "",
		IsSubAgent:           isSubAgent,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		Sessions:             c.sessions,
		Messages:             c.messages,
		Permissions:          c.permissions,
		Cfg:                  c.cfg,
		Tools:                nil,
		Notify:               c.notify,
		RunComplete:          c.runComplete,
		Hooks:                c.hookRunner,
		titles:               &c.titles,
	})

	// The readiness goroutines below perform one-time setup — building the
	// system prompt and the initial tool list — whose results the
	// coordinator needs for its whole lifetime, so they must survive the
	// caller's context being canceled. Several entry points build an agent
	// from a short-lived HTTP request context: the server's
	// InitAgent/UpdateAgent handlers, and UpdateModels -> buildTools ->
	// agentTool -> buildAgent for the subagent. The tool-list build reads
	// the MCP registry as it stands; servers still connecting are picked up
	// by later runs. WithoutCancel drops cancellation while keeping context
	// values; the work is local and always completes.
	//
	// The goroutines join ready, which the caller owns. Only the coordinator
	// constructor may pass c.readyWg: runs Wait on it, and a WaitGroup must
	// not gain work while a Wait is in flight. Agents built mid-session, such
	// as the task subagents rebuilt on every turn, use a group of their own.
	initCtx := context.WithoutCancel(ctx)

	ready.Go(func() error {
		systemPrompt, err := prompt.Build(initCtx, large.Model.Provider(), large.Model.Model(), c.cfg)
		if err != nil {
			return err
		}
		result.SetSystemPrompt(systemPrompt)
		return nil
	})

	ready.Go(func() error {
		tools, err := c.buildTools(initCtx, agent, isSubAgent, func() Model { return models().Large })
		if err != nil {
			return err
		}
		result.SetTools(tools)
		return nil
	})

	return result, nil
}

// effectiveToolNames narrows an agent's configured tool list to the tools
// this coordinator actually attaches. Run mode decides how delegation
// reports back: a TUI can receive a subagent's report as a later turn, a
// headless run exits before one could arrive, so delegation blocks there and
// detaches here. Subagents get neither, since they can neither fan out nor
// ask questions.
func (c *coordinator) effectiveToolNames(agentCfg config.Agent, isSubAgent bool) []string {
	return slices.DeleteFunc(slices.Clone(agentCfg.AllowedTools), func(name string) bool {
		switch name {
		case tools.QuestionToolName:
			return isSubAgent || !c.interactive
		case AgentToolName:
			return isSubAgent || c.interactive
		case AgentDispatchToolName, AgentSendToolName, AgentStatusToolName, AgentStopToolName:
			return isSubAgent || !c.interactive
		}
		return false
	})
}

// buildTools builds an agent's tools. parentModel reports the model the
// agent runs on, which a dispatched subagent inherits.
func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, isSubAgent bool, parentModel func() Model) ([]fantasy.AgentTool, error) {
	allowed := c.effectiveToolNames(agent, isSubAgent)

	var allTools []fantasy.AgentTool
	if slices.Contains(allowed, AgentToolName) {
		agentTool, err := c.agentTool(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agentTool)
	}
	if slices.Contains(allowed, AgentDispatchToolName) {
		dispatchTool, err := c.agentDispatchTool(ctx, agent.ID, parentModel)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, dispatchTool)
	}
	if slices.Contains(allowed, AgentSendToolName) {
		allTools = append(allTools, c.agentSendTool(parentModel))
	}
	if slices.Contains(allowed, AgentStatusToolName) {
		allTools = append(allTools, c.agentStatusTool())
	}
	if slices.Contains(allowed, AgentStopToolName) {
		allTools = append(allTools, c.agentStopTool())
	}

	if slices.Contains(allowed, tools.AgenticFetchToolName) {
		agenticFetchTool, err := c.agenticFetchTool(ctx, nil)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agenticFetchTool)
	}

	// Get the model name for the agent
	modelID := ""
	if modelCfg, ok := c.cfg.Config().Models[agent.Model]; ok {
		if model := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model); model != nil {
			modelID = model.ID
		}
	}

	logFile := filepath.Join(c.cfg.Config().Options.DataDirectory, "logs", "crush.log")

	allTools = append(
		allTools,
		tools.NewBashTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Options.DataDirectory, c.cfg.Config().Options.Attribution, modelID, c.cfg.Config().Options.GetMaxBackgroundJobs(), c.cfg.Config().Permissions, c.shellOwner),
		tools.NewCrushInfoTool(c.cfg, c.lspManager, c.allSkills, c.activeSkills, c.skillTracker),
		tools.NewCrushLogsTool(logFile),
		tools.NewJobOutputTool(c.cfg.Config().Options.DataDirectory),
		tools.NewJobKillTool(),
		tools.NewDownloadTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewMultiEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewFetchTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewGlobTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Glob),
		tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep),
		tools.NewLsTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(nil),
		tools.NewTodosTool(c.sessions),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, c.skillTracker, c.cfg.WorkingDir(), c.cfg.Config().Options.SkillsPaths...),
		tools.NewWriteTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
	)

	// Interactive-only, and never given to subagents; effectiveToolNames
	// is what decides both.
	if slices.Contains(allowed, tools.QuestionToolName) {
		allTools = append(allTools, tools.NewQuestionTool(c.questions))
	}

	// Add LSP tools if user has configured LSPs or auto_lsp is enabled (nil or true).
	if len(c.cfg.Config().LSP) > 0 || c.cfg.Config().Options.AutoLSP == nil || *c.cfg.Config().Options.AutoLSP {
		allTools = append(
			allTools,
			tools.NewDiagnosticsTool(c.lspManager),
			tools.NewReferencesTool(c.lspManager),
			tools.NewLSPRestartTool(c.lspManager),
			tools.NewSymbolsTool(c.lspManager),
			tools.NewDefinitionTool(c.lspManager),
			tools.NewCallHierarchyTool(c.lspManager),
			tools.NewRenameTool(c.lspManager, c.permissions, c.history, c.filetracker),
			tools.NewReplaceSymbolTool(c.lspManager, c.permissions, c.history, c.filetracker),
		)
	}

	if len(c.cfg.Config().MCP) > 0 {
		allTools = append(
			allTools,
			tools.NewListMCPResourcesTool(c.cfg, c.permissions),
			tools.NewReadMCPResourceTool(c.cfg, c.permissions),
		)
	}

	var filteredTools []fantasy.AgentTool
	for _, tool := range allTools {
		if slices.Contains(allowed, tool.Info().Name) {
			filteredTools = append(filteredTools, tool)
		}
	}

	for _, tool := range tools.GetMCPTools(c.permissions, c.cfg, c.cfg.WorkingDir()) {
		if agent.AllowedMCP == nil {
			// No MCP restrictions
			filteredTools = append(filteredTools, tool)
			continue
		}
		if len(agent.AllowedMCP) == 0 {
			// No MCPs allowed
			slog.Debug("No MCPs allowed", "tool", tool.Name(), "agent", agent.Name)
			break
		}

		for mcp, tools := range agent.AllowedMCP {
			if mcp != tool.MCP() {
				continue
			}
			if len(tools) == 0 || slices.Contains(tools, tool.MCPToolName()) {
				filteredTools = append(filteredTools, tool)
				break
			}
			slog.Debug("MCP not allowed", "tool", tool.Name(), "agent", agent.Name)
		}
	}
	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	// Wrap tools with hook interception for the top-level agent only.
	// Subagents (the `agent` task tool, `agentic_fetch`, etc.) run
	// without hook interception to avoid firing the user's hook N times
	// per delegated turn. The top-level invocation of the subagent tool
	// itself is still wrapped from the coder's side.
	filteredTools = wrapToolsWithHooks(filteredTools, c.hookRunner, isSubAgent)

	return filteredTools, nil
}

// buildModel builds the model selected for one slot from the live config.
// isSubAgent marks requests as agent-initiated for providers that bill on it.
func (c *coordinator) buildModel(ctx context.Context, slot config.SelectedModelType, isSubAgent bool) (Model, error) {
	modelCfg, ok := c.cfg.Config().Models[slot]
	if !ok {
		return Model{}, fmt.Errorf("%s %w", slot, errModelNotSelected)
	}
	return c.buildModelFromSelection(ctx, modelCfg, isSubAgent)
}

// buildModelFromSelection builds the model for an explicit selection,
// rather than one of the configured slots. A per-dispatch model override
// goes through here, so an override gets everything a configured model
// gets: provider construction, catalog resolution, request timeouts, and
// the provider-specific wrappers.
func (c *coordinator) buildModelFromSelection(ctx context.Context, modelCfg config.SelectedModel, isSubAgent bool) (Model, error) {
	providerCfg, ok := c.cfg.Config().Providers.Get(modelCfg.Provider)
	if !ok {
		return Model{}, fmt.Errorf("%s %w", modelCfg.Provider, errModelProviderNotConfigured)
	}
	// Resolve the selected model from the built-in Ollama catalog.
	catwalkModel := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model)
	if catwalkModel == nil {
		return Model{}, fmt.Errorf("%s %w", modelCfg.Model, errModelNotFound)
	}

	provider, err := c.buildProvider(providerCfg)
	if err != nil {
		return Model{}, err
	}

	modelID := modelCfg.Model

	languageModel, err := provider.LanguageModel(ctx, modelID)
	if err != nil {
		return Model{}, err
	}

	// End each request when its turn is cancelled, and bound it with the
	// configured timeout so unreachable or hung providers fail instead of
	// blocking a session forever. Retries get a fresh budget each attempt.
	languageModel = newRequestGuardModel(languageModel, c.cfg.Config().Options.GetRequestTimeout())
	languageModel = newRefusalRetryModel(languageModel)

	return Model{
		Model:        languageModel,
		CatwalkCfg:   *catwalkModel,
		ModelCfg:     modelCfg,
		FlatRate:     providerCfg.FlatRate,
		ProviderName: providerCfg.Name,
	}, nil
}

// buildProvider constructs the OpenAI-compatible client used by the
// built-in local Ollama provider. No credentials, gateways or provider SDKs
// are involved.
func (c *coordinator) buildProvider(providerCfg config.ProviderConfig) (fantasy.Provider, error) {
	headers := maps.Clone(providerCfg.ExtraHeaders)
	if headers == nil {
		headers = make(map[string]string)
	}
	baseURL := providerCfg.BaseURL
	if baseURL == "" {
		baseURL = config.OllamaBaseURL
	}

	opts := []openaicompat.Option{
		openaicompat.WithBaseURL(baseURL),
		openaicompat.WithAPIKey(""),
	}
	if c.cfg.Config().Options.Debug {
		opts = append(opts, openaicompat.WithHTTPClient(log.NewHTTPClient()))
	}
	if len(headers) > 0 {
		opts = append(opts, openaicompat.WithHeaders(headers))
	}
	for key, value := range providerCfg.ExtraBody {
		opts = append(opts, openaicompat.WithSDKOptions(openaisdk.WithJSONSet(key, value)))
	}

	return openaicompat.New(opts...)
}

// BeginAccepted reserves an accept slot for sessionID on the active
// agent and returns the ownership handle. It is the fire-and-forget
// dispatch path's only way to mark a run as accepted-but-not-yet-active
// so a cancel arriving before the run registers in activeRequests is not
// lost.
func (c *coordinator) BeginAccepted(sessionID string) *AcceptedRun {
	return c.currentAgent().BeginAccepted(sessionID)
}

func (c *coordinator) Cancel(sessionID string) {
	// Detached subagents outlive the turn that started them, so cancelling
	// the parent's turn leaves them running; they report back when they
	// finish, like a background job. Cancelling a subagent's own session
	// stops that subagent.
	for _, entry := range c.dispatched.Seq2() {
		if entry.SessionID == sessionID {
			entry.stop("cancelled by the user")
		}
	}
	c.currentAgent().Cancel(sessionID)
}

// CancelAll stops everything, detached subagents included. It is the
// shutdown path, so the subagents are stopped without reporting: nobody is
// left to read a report, and the next run on each parent session reports
// them as interrupted instead.
func (c *coordinator) CancelAll() {
	c.currentAgent().CancelAll()
	c.stopAllDispatched(stopForShutdown, 5*time.Second)
}

func (c *coordinator) ClearQueue(sessionID string) {
	c.currentAgent().ClearQueue(sessionID)
}

func (c *coordinator) AddNote(sessionID, text string) {
	c.currentAgent().AddNote(sessionID, text)
}

func (c *coordinator) IsBusy() bool {
	return c.currentAgent().IsBusy()
}

// IsSessionBusy reports whether anything is working on sessionID, subagents
// included. Readers use it to decide whether a half-written session is dead
// and safe to settle, so it must not miss a subagent running on an agent
// instance other than the main one.
func (c *coordinator) IsSessionBusy(sessionID string) bool {
	for _, agent := range c.agents {
		if agent.HasPendingWork(sessionID) {
			return true
		}
	}
	return c.subSessionBusy(sessionID)
}

func (c *coordinator) SetHostContext(hc proto.HostContext) { c.hostContext.Set(hc) }

func (c *coordinator) HostContext() proto.HostContext { return c.hostContext.Get() }

func (c *coordinator) Model() Model {
	return c.currentAgent().Model()
}

func (c *coordinator) UpdateModels(ctx context.Context) error {
	agent, name := c.activeAgent()
	return c.updateAgentModels(ctx, agent, name)
}

// updateAgentModels rebuilds the shared models and the given agent's tools
// from the current config.
func (c *coordinator) updateAgentModels(ctx context.Context, agent SessionAgent, name string) error {
	if err := c.rebuildModels(ctx); err != nil {
		return err
	}

	agentCfg, ok := c.cfg.Config().Agents[name]
	if !ok {
		return fmt.Errorf("%w: %s", errMainAgentNotFound, name)
	}

	tools, err := c.buildTools(ctx, agentCfg, false, agent.Model)
	if err != nil {
		return err
	}
	agent.SetTools(tools)
	return nil
}

func (c *coordinator) QueuedPrompts(sessionID string) int {
	return c.currentAgent().QueuedPrompts(sessionID)
}

func (c *coordinator) QueuedPromptsList(sessionID string) []string {
	return c.currentAgent().QueuedPromptsList(sessionID)
}

func (c *coordinator) Summarize(ctx context.Context, sessionID string) error {
	agent := c.currentAgent()
	providerCfg, ok := c.cfg.Config().Providers.Get(agent.Model().ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}

	opts := getProviderOptions(agent.Model(), providerCfg)
	return agent.Summarize(ctx, sessionID, opts, nil)
}

// GenerateTitle generates a session title using the current agent.
func (c *coordinator) GenerateTitle(ctx context.Context, sessionID, prompt string) {
	agent := c.currentAgent()
	if agent == nil {
		return
	}
	agent.GenerateTitle(ctx, sessionID, prompt)
}

// isUnauthorized reports whether err is an HTTP 401 from a provider.
func isUnauthorized(err error) bool {
	var providerErr *fantasy.ProviderError
	return errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized
}

// subAgentParams holds the parameters for running a subagent.
type subAgentParams struct {
	Agent          SessionAgent
	SessionID      string
	AgentMessageID string
	ToolCallID     string
	Prompt         string
	SessionTitle   string
	// SessionSetup is an optional callback invoked after session creation
	// but before agent execution, for custom session configuration.
	SessionSetup func(sessionID string)
	// SkipCostRollup leaves billing the parent to the caller. Detached
	// subagents roll their cost up themselves after every run, including
	// runs that fail or are stopped part-way.
	SkipCostRollup bool
}

// callTopK always returns nil: Ollama receives top_k through extra_body in
// getProviderOptions, and passing it here too would make Fantasy emit a
// spurious "top_k unsupported" warning for every turn.
func callTopK(_ config.ProviderConfig, _ *int64) *int64 {
	return nil
}

// subAgentCall assembles the call a subagent turn runs under: the model
// settings for the subagent's own model, not the parent's. Both the initial
// task and any message sent to a running subagent go through it, so a
// mid-flight message cannot start a turn with an unconfigured model.
func (c *coordinator) subAgentCall(agent SessionAgent, sessionID, prompt string) (SessionAgentCall, Model, error) {
	model := agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return SessionAgentCall{}, Model{}, errModelProviderNotConfigured
	}

	opts := getProviderOptions(model, providerCfg)

	return SessionAgentCall{
		SessionID:        sessionID,
		Prompt:           prompt,
		MaxOutputTokens:  maxTokens,
		ProviderOptions:  opts,
		Temperature:      model.ModelCfg.Temperature,
		TopP:             model.ModelCfg.TopP,
		TopK:             callTopK(providerCfg, model.ModelCfg.TopK),
		FrequencyPenalty: model.ModelCfg.FrequencyPenalty,
		PresencePenalty:  model.ModelCfg.PresencePenalty,
		NonInteractive:   true,
	}, model, nil
}

// runSubAgent runs a subagent and handles session management and cost accumulation.
// It creates a sub-session, runs the agent with the given prompt, and propagates
// the cost to the parent session.
func (c *coordinator) runSubAgent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	// Create sub-session
	agentToolSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	session, err := c.sessions.CreateTaskSession(ctx, agentToolSessionID, params.SessionID, params.SessionTitle)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("create session: %w", err)
	}

	// A subagent works on its parent's behalf, so it inherits the parent's
	// automatic approval. crush run approves its own session that way, and
	// nobody would be there to answer the subagent's prompts otherwise.
	c.permissions.InheritAutoApproval(params.SessionID, session.ID)

	// Call session setup function if provided
	if params.SessionSetup != nil {
		params.SessionSetup(session.ID)
	}

	call, _, err := c.subAgentCall(params.Agent, session.ID, params.Prompt)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	done := c.trackSubSession(session.ID)
	result, err := params.Agent.Run(ctx, call)
	done()
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to generate response: %s", err)), nil
	}

	// Update parent session cost on a best-effort basis. A failure here must
	// not discard the subagent output that was already produced.
	if !params.SkipCostRollup {
		if err := c.updateParentSessionCost(ctx, session.ID, params.SessionID); err != nil {
			slog.Warn(
				"Failed to update parent session cost",
				"child_session", session.ID,
				"parent_session", params.SessionID,
				"error", err,
			)
		}
	}

	return subAgentResponse(result), nil
}

const (
	subAgentNoOutput  = "Subagent completed but produced no text output."
	subAgentRefused   = "Subagent stopped early: the provider's safety classifier refused its response, so it never finished the task. The same task on the same model will likely be refused again; rephrase it or dispatch a new subagent on a different model."
	subAgentTruncated = "Subagent stopped early: it ran out of output tokens mid-response, so it never finished the task. Ask it to continue in smaller steps."
)

// subAgentResponse turns a finished subagent run into the parent's tool
// result. The run's last step decides: fantasy falls back to an earlier
// step's text when the last one has none, which after a cut-short step is
// narration, not a report.
func subAgentResponse(result *fantasy.AgentResult) fantasy.ToolResponse {
	if result == nil {
		return fantasy.NewTextErrorResponse(subAgentNoOutput)
	}
	var last fantasy.StepResult
	if n := len(result.Steps); n > 0 {
		last = result.Steps[n-1]
	}
	switch last.FinishReason {
	case fantasy.FinishReasonContentFilter:
		return fantasy.NewTextErrorResponse(withLastWords(subAgentRefused, last))
	case fantasy.FinishReasonLength:
		return fantasy.NewTextErrorResponse(withLastWords(subAgentTruncated, last))
	}
	if output := result.Response.Content.Text(); output != "" {
		return fantasy.NewTextResponse(output)
	}
	return fantasy.NewTextErrorResponse(subAgentNoOutput)
}

// withLastWords appends whatever text the cut-short step did produce.
func withLastWords(reason string, step fantasy.StepResult) string {
	text := strings.TrimSpace(step.Content.Text())
	if text == "" {
		return reason
	}
	return reason + "\n\nIts last output before stopping:\n\n" + text
}

// updateParentSessionCost accumulates the cost from a child session to its parent session.
func (c *coordinator) updateParentSessionCost(ctx context.Context, childSessionID, parentSessionID string) error {
	childSession, err := c.sessions.Get(ctx, childSessionID)
	if err != nil {
		return fmt.Errorf("get child session: %w", err)
	}

	parentSession, err := c.sessions.Get(ctx, parentSessionID)
	if err != nil {
		return fmt.Errorf("get parent session: %w", err)
	}

	parentSession.Cost += childSession.Cost

	if _, err := c.sessions.Save(ctx, parentSession); err != nil {
		return fmt.Errorf("save parent session: %w", err)
	}

	return nil
}

// discoverSkills is a thin fallback wrapper used only when no
// skills.Manager has been threaded through to the coordinator. All
// production call sites (backend.CreateWorkspace, setupLocalWorkspace)
// run discovery in advance and pass the results via the manager;
// reaching this path means a caller bypassed both. It deliberately does
// NOT publish to the package-level broker — there are no subscribers in
// that case, so doing so would be misleading without delivering the
// snapshot anywhere useful.
func discoverSkills(cfg *config.ConfigStore) (allSkills, activeSkills []*skills.Skill) {
	opts := cfg.Config().Options
	var paths, disabled []string
	if opts != nil {
		paths = opts.SkillsPaths
		disabled = opts.DisabledSkills
	}
	var resolver func(string) (string, error)
	if r := cfg.Resolver(); r != nil {
		resolver = r.ResolveValue
	}
	allSkills, activeSkills, states := skills.DiscoverFromConfig(skills.DiscoveryConfig{
		SkillsPaths:    paths,
		DisabledSkills: disabled,
		Resolver:       resolver,
	})
	logDiscoveryStats(states, paths, allSkills, activeSkills, disabled)
	return allSkills, activeSkills
}

// logTurnSkillUsage emits a per-turn diagnostic line showing which skills
// (if any) were loaded during this turn and which looked relevant based on
// a cheap keyword match against the user prompt. The goal is to surface
// "should-have-loaded but didn't" situations for later analysis.
//
// Logged at Info level under component=skills; heavy fields are elided when
// there is nothing interesting to report.
func logTurnSkillUsage(
	sessionID string,
	prompt string,
	activeSkills []*skills.Skill,
	tracker *skills.Tracker,
	before []string,
) {
	if tracker == nil || len(activeSkills) == 0 {
		return
	}

	after := tracker.LoadedNames()

	beforeSet := make(map[string]bool, len(before))
	for _, n := range before {
		beforeSet[n] = true
	}
	var loadedThisTurn []string
	for _, n := range after {
		if !beforeSet[n] {
			loadedThisTurn = append(loadedThisTurn, n)
		}
	}

	slog.Info(
		"Skill turn summary",
		"component", "skills",
		"session_id", sessionID,
		"prompt_len", len(prompt),
		"active_total", len(activeSkills),
		"loaded_total", len(after),
		"loaded_this_turn", loadedThisTurn,
	)
}

// logDiscoveryStats emits a single structured log line summarising skill
// discovery for the current session. It is intentionally low-volume: one
// line per session start. Builtin vs user counts are derived from the
// SkillState.Path — builtin states use the "builtin/" embed prefix.
func logDiscoveryStats(
	states []*skills.SkillState,
	userPaths []string,
	allSkills, activeSkills []*skills.Skill,
	disabled []string,
) {
	var builtinOK, builtinErr, userOK, userErr int
	for _, s := range states {
		isBuiltin := strings.HasPrefix(s.Path, "builtin/")
		switch {
		case isBuiltin && s.State == skills.StateNormal:
			builtinOK++
		case isBuiltin && s.State == skills.StateError:
			builtinErr++
		case !isBuiltin && s.State == skills.StateNormal:
			userOK++
		case !isBuiltin && s.State == skills.StateError:
			userErr++
		}
	}

	activeNames := make([]string, 0, len(activeSkills))
	for _, s := range activeSkills {
		activeNames = append(activeNames, s.Name)
	}

	xml := skills.ToPromptXML(activeSkills)

	slog.Info(
		"Skill discovery complete",
		"component", "skills",
		"builtin_ok", builtinOK,
		"builtin_errors", builtinErr,
		"user_ok", userOK,
		"user_errors", userErr,
		"user_paths", len(userPaths),
		"deduped_total", len(allSkills),
		"active", len(activeSkills),
		"disabled", len(disabled),
		"prompt_bytes", len(xml),
		"prompt_tok_est", skills.ApproxTokenCount(xml),
		"active_names", activeNames,
	)
}
