package channel

import (
	"context"
	"fmt"
	"time"

	"github.com/VaalaCat/ai-gateway/internal/dao"
	"github.com/VaalaCat/ai-gateway/internal/models"
	"github.com/VaalaCat/ai-gateway/internal/pkg/app"
	"github.com/VaalaCat/ai-gateway/internal/pkg/copilot"
	"github.com/VaalaCat/ai-gateway/internal/pkg/events"
	"github.com/VaalaCat/ai-gateway/internal/pkg/httputil"
)

func ensureCopilotModelPrice(ctx context.Context, daoCtx dao.Context, bus app.EventBus, channel models.Channel, model string) (models.Channel, error) {
	if _, ok := copilot.PremiumCostFromOtherSettings(channel.OtherSettings, model); ok {
		return channel, nil
	}

	client := httputil.NewClient(channel.ProxyURL, 15*time.Second)
	enterpriseDomain := copilot.EnterpriseDomainFromOtherSettings(channel.OtherSettings)
	catalog, err := copilot.FetchModelCatalog(ctx, client, channel.GetBaseURL(), channel.Key, enterpriseDomain)
	if err != nil {
		return channel, fmt.Errorf("fetch github copilot model prices failed: %w", err)
	}

	mergedChannel, err := persistCopilotModelPrices(daoCtx, bus, channel, enterpriseDomain, copilot.ModelPricesFromCatalog(catalog))
	if err != nil {
		return channel, err
	}
	if _, ok := copilot.PremiumCostFromOtherSettings(mergedChannel.OtherSettings, model); !ok {
		return channel, fmt.Errorf("github copilot premium cost is not available for model %q", model)
	}
	return mergedChannel, nil
}

func persistCopilotModelPrices(daoCtx dao.Context, bus app.EventBus, channel models.Channel, enterpriseDomain string, prices map[string]float64) (models.Channel, error) {
	merged, err := copilot.MergeOtherSettings(channel.OtherSettings, copilot.OtherSettings{
		EnterpriseDomain: enterpriseDomain,
		ModelPrices:      prices,
	})
	if err != nil {
		return channel, fmt.Errorf("merge github copilot model prices failed: %w", err)
	}
	if merged == channel.OtherSettings {
		channel.OtherSettings = merged
		return channel, nil
	}

	q := dao.NewAdminQuery(daoCtx)
	m := dao.NewAdminMutation(daoCtx)
	if err := m.Channel().Update(channel.ID, map[string]any{"other_settings": merged}); err != nil {
		return channel, fmt.Errorf("persist github copilot model prices failed: %w", err)
	}
	refreshed, err := q.Channel().GetByID(channel.ID)
	if err != nil {
		return channel, fmt.Errorf("reload github copilot channel failed: %w", err)
	}
	if err := events.PublishChannelUpdate(context.Background(), bus, *refreshed); err != nil {
		return *refreshed, fmt.Errorf("publish github copilot channel prices failed: %w", err)
	}
	return *refreshed, nil
}
