package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionStatus_Dormant_RoundTrips(t *testing.T) {
	t.Parallel()

	sess := AgentSession{
		UUID:    "test-uuid",
		Agent:   "claude-code",
		Status:  SessionStatusDormant,
		Started: time.Now().UTC().Truncate(time.Second),
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AgentSession
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != SessionStatusDormant {
		t.Errorf("Status = %q, want %q", got.Status, SessionStatusDormant)
	}
}
