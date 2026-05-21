package channel

import (
	"github.com/VaalaCat/ai-gateway/internal/master/api"
	"github.com/VaalaCat/ai-gateway/internal/pkg/app"
	"github.com/VaalaCat/ai-gateway/internal/pkg/copilot"
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
