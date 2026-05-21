package channel

import (
	"strconv"
	"time"

	"github.com/VaalaCat/ai-gateway/internal/consts"
	"github.com/VaalaCat/ai-gateway/internal/dao"
	"github.com/VaalaCat/ai-gateway/internal/master/api"
	"github.com/VaalaCat/ai-gateway/internal/pkg/app"
	"github.com/VaalaCat/ai-gateway/internal/pkg/copilot"
	"github.com/VaalaCat/ai-gateway/internal/pkg/httputil"
)

func (h *Handler) CopilotDeviceStart(c *app.Context, req CopilotDeviceStartRequest) (CopilotDeviceStartResponse, error) {
	resp, err := copilot.StartDeviceFlow(c.Request.Context(), copilot.DeviceStartRequest{
		ClientID:      c.Settings.Master.CopilotOAuthClientID,
		EnterpriseURL: req.EnterpriseURL,
	})
	if err != nil {
		return CopilotDeviceStartResponse{}, api.BadRequestError(err.Error(), err)
	}
	return CopilotDeviceStartResponse(resp), nil
}

func (h *Handler) CopilotDevicePoll(c *app.Context, req CopilotDevicePollRequest) (CopilotDevicePollResponse, error) {
	resp, err := copilot.PollDeviceFlow(c.Request.Context(), copilot.DevicePollRequest{
		ClientID:      c.Settings.Master.CopilotOAuthClientID,
		DeviceCode:    req.DeviceCode,
		EnterpriseURL: req.EnterpriseURL,
	})
	if err != nil {
		return CopilotDevicePollResponse{}, api.BadRequestError(err.Error(), err)
	}
	return CopilotDevicePollResponse(resp), nil
}

func (h *Handler) CopilotQuota(c *app.Context, req CopilotQuotaRequest) (CopilotQuotaResponse, error) {
	id, _ := strconv.ParseUint(req.ID, 10, 64)
	daoCtx := dao.NewContext(c.App)
	q := dao.NewAdminQuery(daoCtx)

	channel, err := q.Channel().GetByID(uint(id))
	if err != nil {
		return CopilotQuotaResponse{}, api.NotFoundError(consts.ErrNotFound)
	}
	if channel.Type != consts.ChannelTypeGitHubCopilot {
		return CopilotQuotaResponse{}, api.BadRequestError("channel is not GitHub Copilot", nil)
	}
	client := httputil.NewClient(channel.ProxyURL, 15*time.Second)
	enterpriseDomain := copilot.EnterpriseDomainFromOtherSettings(channel.OtherSettings)
	quota, err := copilot.FetchQuota(c.Request.Context(), client, enterpriseDomain, channel.Key)
	if err != nil {
		return CopilotQuotaResponse{}, api.BadRequestError(err.Error(), err)
	}
	catalog, err := copilot.FetchModelCatalog(c.Request.Context(), client, channel.GetBaseURL(), channel.Key, enterpriseDomain)
	modelPricesError := ""
	if err != nil {
		modelPricesError = err.Error()
	} else if len(catalog) > 0 {
		if _, err := persistCopilotModelPrices(daoCtx, c.GetBus(), *channel, enterpriseDomain, copilot.ModelPricesFromCatalog(catalog)); err != nil {
			modelPricesError = err.Error()
		}
	}
	return copilotQuotaResponse(quota, catalog, modelPricesError), nil
}

func copilotQuotaResponse(quota copilot.Quota, catalog []copilot.ModelInfo, modelPricesError string) CopilotQuotaResponse {
	return CopilotQuotaResponse{
		PlanType:         quota.PlanType,
		QuotaResetAt:     quota.QuotaResetAt,
		Premium:          copilotQuotaSnapshotResponse(quota.Premium),
		Chat:             copilotQuotaSnapshotResponse(quota.Chat),
		Completions:      copilotQuotaSnapshotResponse(quota.Completions),
		ModelPrices:      copilotModelPricesResponse(catalog),
		ModelPricesError: modelPricesError,
		LastUpdatedAt:    quota.LastUpdatedAt,
	}
}

func copilotModelPricesResponse(catalog []copilot.ModelInfo) []CopilotModelPrice {
	prices := make([]CopilotModelPrice, 0, len(catalog))
	for _, item := range catalog {
		if !item.PremiumCostKnown {
			continue
		}
		prices = append(prices, CopilotModelPrice{ModelName: item.ID, PremiumCost: item.PremiumCost})
	}
	return prices
}

func copilotQuotaSnapshotResponse(snapshot copilot.QuotaSnapshot) copilotQuotaSnapshot {
	return copilotQuotaSnapshot{
		Reported:         snapshot.Reported,
		Entitlement:      snapshot.Entitlement,
		Remaining:        snapshot.Remaining,
		Used:             snapshot.Used,
		PercentRemaining: snapshot.PercentRemaining,
		PercentUsed:      snapshot.PercentUsed,
		Unlimited:        snapshot.Unlimited,
		OverageCount:     snapshot.OverageCount,
		OveragePermitted: snapshot.OveragePermitted,
		QuotaID:          snapshot.QuotaID,
	}
}
