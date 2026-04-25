package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sausheong/cortex"
	"github.com/sausheong/felix/internal/agent"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tools"
)

// AgentRunnerImpl implements tools.AgentRunner by constructing a fresh
// agent runtime for the target agent and running it synchronously.
// The delegated agent gets core tools only — ask_agent is NOT registered
// to prevent infinite recursion.
type AgentRunnerImpl struct {
	mu            sync.RWMutex // guards providers, config, compactionMgr against hot-reload
	providers     map[string]llm.LLMProvider
	config        *config.Config
	sessionStore  *session.Store
	skills        *skill.Loader
	memory        *memory.Manager
	cortex        *cortex.Cortex
	compactionMgr *compaction.Manager // shared across all delegated runtimes
}

// NewAgentRunner creates an AgentRunnerImpl.
func NewAgentRunner(
	providers map[string]llm.LLMProvider,
	cfg *config.Config,
	sessionStore *session.Store,
) *AgentRunnerImpl {
	return &AgentRunnerImpl{
		providers:     providers,
		config:        cfg,
		sessionStore:  sessionStore,
		compactionMgr: compaction.BuildManager(cfg),
	}
}

// SetSkills sets the skill loader for delegated agents.
func (r *AgentRunnerImpl) SetSkills(skills *skill.Loader) {
	r.skills = skills
}

// SetMemory sets the memory manager for delegated agents.
func (r *AgentRunnerImpl) SetMemory(mem *memory.Manager) {
	r.memory = mem
}

// SetCortex sets the Cortex knowledge graph for delegated agents.
func (r *AgentRunnerImpl) SetCortex(cx *cortex.Cortex) {
	r.cortex = cx
}

// UpdateProviders swaps the LLM provider map atomically. Called by the config
// watcher after the user edits provider credentials in the Settings UI so
// delegated runs see the new API key / base URL without a restart.
func (r *AgentRunnerImpl) UpdateProviders(providers map[string]llm.LLMProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
}

// UpdateConfig hot-reloads the config and rebuilds the shared compaction
// Manager so delegated runs pick up new agents, tool policies, and
// compaction settings without a restart.
func (r *AgentRunnerImpl) UpdateConfig(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config = cfg
	r.compactionMgr = compaction.BuildManager(cfg)
}

// RunAgent delegates a task to the specified agent and returns the text response.
func (r *AgentRunnerImpl) RunAgent(ctx context.Context, agentID, prompt string) (string, error) {
	r.mu.RLock()
	cfg := r.config
	provMap := r.providers
	compactionMgr := r.compactionMgr
	skills := r.skills
	memMgr := r.memory
	cx := r.cortex
	r.mu.RUnlock()

	agentCfg, ok := cfg.GetAgent(agentID)
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentID)
	}

	providerName, modelName := llm.ParseProviderModel(agentCfg.Model)
	provider, ok := provMap[providerName]
	if !ok {
		return "", fmt.Errorf("provider %q not available for agent %q", providerName, agentID)
	}

	// Build a fresh tool registry with core tools only — no ask_agent
	// to prevent infinite delegation recursion.
	delegateToolReg := tools.NewRegistry()
	execPolicy := &tools.ExecPolicy{
		Level:     cfg.Security.ExecApprovals.Level,
		Allowlist: cfg.Security.ExecApprovals.Allowlist,
	}
	tools.RegisterCoreTools(delegateToolReg, agentCfg.Workspace, execPolicy)

	// Apply the target agent's tool policy
	var executor tools.Executor = delegateToolReg
	if len(agentCfg.Tools.Allow) > 0 || len(agentCfg.Tools.Deny) > 0 {
		executor = tools.NewFilteredRegistry(delegateToolReg, tools.Policy{
			Allow: agentCfg.Tools.Allow,
			Deny:  agentCfg.Tools.Deny,
		})
	}

	// Use a dedicated session so delegated work doesn't pollute channel sessions
	sess := session.NewSession(agentID, fmt.Sprintf("delegate_%s", agentID))

	rt := &agent.Runtime{
		LLM:          provider,
		Tools:        executor,
		Session:      sess,
		AgentID:      agentCfg.ID,
		AgentName:    agentCfg.Name,
		Model:        modelName,
		Workspace:    agentCfg.Workspace,
		MaxTurns:     agentCfg.MaxTurns,
		SystemPrompt: agentCfg.SystemPrompt,
		Skills:       skills,
		Memory:       memMgr,
		Cortex:       cx,
		Compaction:   compactionMgr,
	}

	slog.Info("delegating to agent", "agent", agentID, "prompt_len", len(prompt))
	response, err := rt.RunSync(ctx, prompt, nil)
	if err != nil {
		return "", fmt.Errorf("agent %q execution failed: %w", agentID, err)
	}

	return response, nil
}

// AvailableAgents returns the list of configured agents.
func (r *AgentRunnerImpl) AvailableAgents() []tools.AgentInfo {
	r.mu.RLock()
	agents := r.config.Agents.List
	r.mu.RUnlock()
	infos := make([]tools.AgentInfo, len(agents))
	for i, a := range agents {
		infos[i] = tools.AgentInfo{
			ID:   a.ID,
			Name: a.Name,
		}
	}
	return infos
}
