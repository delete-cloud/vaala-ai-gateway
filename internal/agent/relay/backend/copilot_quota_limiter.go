package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sync"

	"github.com/VaalaCat/ai-gateway/internal/agent/relay/upstream"
	"github.com/VaalaCat/ai-gateway/internal/models"
	"github.com/VaalaCat/ai-gateway/internal/pkg/app"
	"github.com/VaalaCat/ai-gateway/internal/pkg/copilot"
)

const copilotQuotaEpsilon = 1e-9

type copilotQuotaFetcher func(ctx context.Context, ch *models.Channel, pool app.TransportPool) (copilot.Quota, error)

type copilotQuotaReservation func(success bool)

type copilotQuotaLimiter interface {
	Reserve(ctx context.Context, ch *models.Channel, pool app.TransportPool, cost float64) (copilotQuotaReservation, error)
}

type accountCopilotQuotaLimiter struct {
	mu     sync.Mutex
	fetch  copilotQuotaFetcher
	states map[string]*copilotQuotaState
}

type copilotQuotaState struct {
	resetAt       string
	quotaID       string
	lastRemaining float64
	localUsed     float64
	inFlight      float64
}

func newAccountCopilotQuotaLimiter(fetch copilotQuotaFetcher) *accountCopilotQuotaLimiter {
	return &accountCopilotQuotaLimiter{
		fetch:  fetch,
		states: make(map[string]*copilotQuotaState),
	}
}

func (l *accountCopilotQuotaLimiter) Reserve(ctx context.Context, ch *models.Channel, pool app.TransportPool, cost float64) (copilotQuotaReservation, error) {
	if cost <= 0 {
		return noopCopilotQuotaReservation, nil
	}
	if ch == nil {
		return nil, fmt.Errorf("github copilot channel is required for quota check")
	}
	quota, err := l.fetch(ctx, ch, pool)
	if err != nil {
		return nil, fmt.Errorf("github copilot quota check failed: %w", err)
	}
	premium := quota.Premium
	if premium.Unlimited {
		return noopCopilotQuotaReservation, nil
	}
	if !premium.Reported || premium.Entitlement <= 0 {
		return nil, fmt.Errorf("github copilot premium quota is not available")
	}

	key := copilotQuotaKey(ch)
	l.mu.Lock()
	defer l.mu.Unlock()

	st := l.states[key]
	if st == nil {
		st = &copilotQuotaState{}
		l.states[key] = st
	}
	st.reconcile(quota.QuotaResetAt, premium.QuotaID, premium.Remaining)

	remaining := math.Min(math.Max(premium.Remaining, 0), premium.Entitlement)
	available := remaining - st.localUsed - st.inFlight
	if cost > available+copilotQuotaEpsilon {
		return nil, fmt.Errorf("github copilot premium quota exceeded: required %.2f, available %.2f, remaining %.2f", cost, math.Max(available, 0), remaining)
	}
	st.inFlight += cost

	var once sync.Once
	return func(success bool) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			st.inFlight = math.Max(0, st.inFlight-cost)
			if success {
				st.localUsed += cost
			}
		})
	}, nil
}

func (s *copilotQuotaState) reconcile(resetAt, quotaID string, remaining float64) {
	if s.resetAt != resetAt || s.quotaID != quotaID || remaining > s.lastRemaining+copilotQuotaEpsilon {
		s.resetAt = resetAt
		s.quotaID = quotaID
		s.lastRemaining = remaining
		s.localUsed = 0
		s.inFlight = 0
		return
	}
	acknowledged := s.lastRemaining - remaining
	if acknowledged > copilotQuotaEpsilon {
		s.localUsed = math.Max(0, s.localUsed-acknowledged)
		s.lastRemaining = remaining
	}
}

func noopCopilotQuotaReservation(bool) {}

func defaultCopilotQuotaFetch(ctx context.Context, ch *models.Channel, pool app.TransportPool) (copilot.Quota, error) {
	client := upstream.BuildHTTPClient(pool, ch)
	enterpriseDomain := copilot.EnterpriseDomainFromOtherSettings(ch.OtherSettings)
	return copilot.FetchQuota(ctx, client, enterpriseDomain, ch.Key)
}

func copilotQuotaKey(ch *models.Channel) string {
	tokenHash := sha256.Sum256([]byte(ch.Key))
	return fmt.Sprintf("%d:%s", ch.ID, hex.EncodeToString(tokenHash[:8]))
}
