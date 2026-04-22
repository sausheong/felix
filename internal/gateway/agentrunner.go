package gateway

import (
	"context"
	"fmt"
	"log/slog"

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

// RunAgent delegates a task to the specified agent and returns the text response.
func (r *AgentRunnerImpl) RunAgent(ctx context.Context, agentID, prompt string) (string, error) {
	agentCfg, ok := r.config.GetAgent(agentID)
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentID)
	}

	providerName, modelName := llm.ParseProviderModel(agentCfg.Model)
	provider, ok := r.providers[providerName]
	if !ok {
		return "", fmt.Errorf("provider %q not available for agent %q", providerName, agentID)
	}

	// Build a fresh tool registry with core tools only — no ask_agent
	// to prevent infinite delegation recursion.
	delegateToolReg := tools.NewRegistry()
	execPolicy := &tools.ExecPolicy{
		Level:     r.config.Security.ExecApprovals.Level,
		Allowlist: r.config.Security.ExecApprovals.Allowlist,
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
		Skills:       r.skills,
		Memory:       r.memory,
		Cortex:       r.cortex,
		Compaction:   r.compactionMgr,
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
	agents := r.config.Agents.List
	infos := make([]tools.AgentInfo, len(agents))
	for i, a := range agents {
		infos[i] = tools.AgentInfo{
			ID:   a.ID,
			Name: a.Name,
		}
	}
	return infos
}
