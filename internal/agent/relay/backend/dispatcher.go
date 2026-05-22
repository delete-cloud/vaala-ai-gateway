package backend

import (
	"fmt"

	"github.com/VaalaCat/ai-gateway/internal/agent/relay/backend/legacy"
	"github.com/VaalaCat/ai-gateway/internal/agent/relay/backend/native"
	"github.com/VaalaCat/ai-gateway/internal/agent/relay/backend/passthrough"
	"github.com/VaalaCat/ai-gateway/internal/agent/relay/state"
	"github.com/VaalaCat/ai-gateway/internal/agent/relay/upstream"
	"github.com/VaalaCat/ai-gateway/internal/consts"
	"github.com/VaalaCat/ai-gateway/internal/pkg/app"
	"github.com/VaalaCat/ai-gateway/internal/pkg/copilot"
)

// Dispatcher 按 Attempt.Mode 选择后端执行，并在结果上叠加统一的 token 计数调和。
// 等价老 Handler 主循环里 useLegacy/shouldPassthrough 三分支 + reconcileUsage 的组合，
// 但策略表化了路径选择，把 mode→backend 的扩展点从 if/else 解耦。
//
// Backends 字段导出方便测试构造，生产链路统一走 NewDispatcher。
type Dispatcher struct {
	Backends map[state.RelayMode]Backend
}

// NewDispatcher 注册 3 个内置 backend 并把 agent 注入到所有 backend。
// agent 允许为 nil（测试场景），backend 内部各 Getter 会做 nil 守卫。
func NewDispatcher(agent app.AgentApplication) *Dispatcher {
	return &Dispatcher{
		Backends: map[state.RelayMode]Backend{
			state.ModeNative:      &native.Backend{Agent: agent},
			state.ModeLegacy:      &legacy.Backend{Agent: agent},
			state.ModePassthrough: &passthrough.Backend{Agent: agent},
		},
	}
}

// Dispatch 单次 attempt 派发：按 mode 取 backend 执行 → 用 FinalizeTokenCounts 调和 token。
// 未注册的 mode 返回带 error 的零值结果，绝不 panic。
func (d *Dispatcher) Dispatch(rctx *state.RelayContext, a state.Attempt) state.AttemptResult {
	backend, ok := d.Backends[a.Mode]
	if !ok {
		return state.AttemptResult{Err: fmt.Errorf("no backend for mode %q", string(a.Mode))}
	}
	copilotCostUnits := 0
	if a.Channel != nil && a.Channel.Type == consts.ChannelTypeGitHubCopilot {
		cost, ok := copilot.PremiumCostUnitsFromOtherSettings(a.Channel.OtherSettings, a.RealModel)
		if !ok {
			return state.AttemptResult{Err: fmt.Errorf("github copilot premium cost is not configured for model %q", a.RealModel)}
		}
		copilotCostUnits = cost
	}
	raw := backend.Relay(rctx, a)
	if a.Channel != nil && a.Channel.Type == consts.ChannelTypeGitHubCopilot {
		return countCopilotRequest(raw, copilotCostUnits)
	}
	final := upstream.FinalizeTokenCounts(rctx.Input.Body, raw.PromptTokens, raw.CompletionTokens, raw.ResponseText, a.RealModel)
	raw.PromptTokens = final.PromptTokens
	raw.CompletionTokens = final.CompletionTokens
	raw.TokenSource = string(final.Source)
	return raw
}

func countCopilotRequest(raw state.AttemptResult, costUnits int) state.AttemptResult {
	if raw.Err != nil {
		raw.PromptTokens = 0
		raw.CompletionTokens = 0
		raw.CacheReadTokens = 0
		raw.CacheWriteTokens = 0
		raw.TokenSource = ""
		return raw
	}
	raw.PromptTokens = costUnits
	raw.CompletionTokens = 0
	raw.CacheReadTokens = 0
	raw.CacheWriteTokens = 0
	raw.TokenSource = "copilot_request"
	return raw
}
