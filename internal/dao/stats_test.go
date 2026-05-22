package dao

import (
	"testing"
	"time"

	"github.com/VaalaCat/ai-gateway/internal/models"
)

func TestStatsDAO(t *testing.T) {
	ctx, db := setupAdminContext(t)
	q := NewAdminQuery(ctx).Stats()

	// Seed data
	db.Create(&models.User{Username: "u1"})
	db.Create(&models.User{Username: "u2"})
	db.Create(&models.Token{UserID: 1, Key: "k1", Name: "t1"})
	db.Create(&models.Channel{Name: "ch1", Type: 1})
	db.Create(&models.Agent{AgentID: "a1", Name: "agent1"})
	db.Create(&models.ModelConfig{ModelName: "gpt-4"})

	now := time.Now().Unix()
	db.Select("*").Create(&models.UsageLog{UserID: 1, RequestID: "r1", TotalCost: 100, CreatedAt: now})
	db.Select("*").Create(&models.UsageLog{UserID: 2, RequestID: "r2", TotalCost: 250, CreatedAt: now})

	t.Run("GetOverview", func(t *testing.T) {
		s, err := q.GetOverview()
		if err != nil {
			t.Fatalf("GetOverview: %v", err)
		}
		if s.UserCount != 2 {
			t.Fatalf("expected 2 users, got %v", s.UserCount)
		}
		if s.TokenCount != 1 {
			t.Fatalf("expected 1 token, got %v", s.TokenCount)
		}
		if s.ChannelCount != 1 {
			t.Fatalf("expected 1 channel, got %v", s.ChannelCount)
		}
		if s.AgentCount != 1 {
			t.Fatalf("expected 1 agent, got %v", s.AgentCount)
		}
		if s.ModelConfigCount != 1 {
			t.Fatalf("expected 1 model config, got %v", s.ModelConfigCount)
		}
		if s.UsageLogCount != 2 {
			t.Fatalf("expected 2 usage logs, got %v", s.UsageLogCount)
		}
		if s.TotalCost != 350 {
			t.Fatalf("expected total cost 350, got %v", s.TotalCost)
		}
	})

	t.Run("GetTableCount", func(t *testing.T) {
		count, err := q.GetTableCount(TableUsers)
		if err != nil {
			t.Fatalf("GetTableCount: %v", err)
		}
		if count != 2 {
			t.Fatalf("expected 2, got %v", count)
		}
	})

	t.Run("GetTotalCost no filter", func(t *testing.T) {
		cost, err := q.GetTotalCost(UsageLogListFilter{})
		if err != nil {
			t.Fatalf("GetTotalCost: %v", err)
		}
		if cost != 350 {
			t.Fatalf("expected 350, got %v", cost)
		}
	})

	t.Run("GetTotalCost with UserID filter", func(t *testing.T) {
		uid := uint(1)
		cost, err := q.GetTotalCost(UsageLogListFilter{UserID: &uid})
		if err != nil {
			t.Fatalf("GetTotalCost: %v", err)
		}
		if cost != 100 {
			t.Fatalf("expected 100, got %v", cost)
		}
	})

	t.Run("GetTotalCost empty result", func(t *testing.T) {
		uid := uint(9999)
		cost, err := q.GetTotalCost(UsageLogListFilter{UserID: &uid})
		if err != nil {
			t.Fatalf("GetTotalCost: %v", err)
		}
		if cost != 0 {
			t.Fatalf("expected 0, got %v", cost)
		}
	})

	t.Run("GetTrend", func(t *testing.T) {
		items, err := q.GetTrend(30, nil)
		if err != nil {
			t.Fatalf("GetTrend: %v", err)
		}
		if len(items) == 0 {
			t.Fatal("expected at least one trend item")
		}
		total := 0.0
		for _, item := range items {
			total += item.Cost
		}
		if total != 350 {
			t.Fatalf("expected total cost 350, got %v", total)
		}
	})

	t.Run("GetTrend with userID", func(t *testing.T) {
		uid := uint(1)
		items, err := q.GetTrend(30, &uid)
		if err != nil {
			t.Fatalf("GetTrend: %v", err)
		}
		total := 0.0
		for _, item := range items {
			total += item.Cost
		}
		if total != 100 {
			t.Fatalf("expected total cost 100, got %v", total)
		}
	})
}
