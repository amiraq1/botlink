package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func openBenchStore(b *testing.B) *Store {
	b.Helper()
	st, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return st
}

func benchMembers(n int) []MemberInput {
	out := make([]MemberInput, n)
	for i := range out {
		out[i] = MemberInput{
			Name:        "Bot" + strconv.Itoa(i+1),
			RoleTitle:   "role",
			AccentColor: palette[i%len(palette)],
		}
	}
	return out
}

func createBenchGroup(b *testing.B, st *Store, now int64, members int, maxTurns int64) int64 {
	b.Helper()
	id, err := st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "Bench", Description: "d", Topic: "t", TurnMode: "auto_round_robin",
		MaxTurns: maxTurns, Members: benchMembers(members),
	}, now)
	if err != nil {
		b.Fatalf("CreateGroup: %v", err)
	}
	return id
}

func seedBenchMessages(b *testing.B, st *Store, groupID int64, n int, now int64) {
	b.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := st.SendLeaderMessage(ctx, groupID, "message "+strconv.Itoa(i), now); err != nil {
			b.Fatalf("seed messages: %v", err)
		}
	}
}

func markBenchOnline(b *testing.B, st *Store, token string, now int64) {
	b.Helper()
	if _, err := st.db.ExecContext(context.Background(),
		"UPDATE bot_members SET last_seen_at = ? WHERE token_hash = ? OR agent_token = ?", now, HashToken(token), token); err != nil {
		b.Fatalf("mark online: %v", err)
	}
}

// BenchmarkListGroups measures the admin groups home query with 50 groups,
// 5 members each and 20 messages per group.
func BenchmarkListGroups(b *testing.B) {
	st := openBenchStore(b)
	now := time.Now().UnixMilli()
	for i := 0; i < 50; i++ {
		id := createBenchGroup(b, st, now, 5, 20)
		seedBenchMessages(b, st, id, 20, now)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		groups, err := st.ListGroups(ctx, now)
		if err != nil {
			b.Fatal(err)
		}
		if len(groups) != 50 {
			b.Fatalf("groups = %d; want 50", len(groups))
		}
	}
}

// BenchmarkGetGroupDetailPaged measures one admin chat poll over 500 buffered
// messages (the client page size is 100).
func BenchmarkGetGroupDetailPaged(b *testing.B) {
	st := openBenchStore(b)
	now := time.Now().UnixMilli()
	id := createBenchGroup(b, st, now, 5, 500)
	seedBenchMessages(b, st, id, 500, now)
	d, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		b.Fatal(err)
	}
	for _, m := range d.Members {
		markBenchOnline(b, st, m.AgentToken, now)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.GetGroupDetailPaged(ctx, id, now, 0, 0, 100); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetRoomState measures one public agent-room poll.
func BenchmarkGetRoomState(b *testing.B) {
	st := openBenchStore(b)
	now := time.Now().UnixMilli()
	id := createBenchGroup(b, st, now, 5, 500)
	seedBenchMessages(b, st, id, 200, now)
	d, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		b.Fatal(err)
	}
	token := tokenOf(d, "bot_1")
	for _, m := range d.Members {
		markBenchOnline(b, st, m.AgentToken, now)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		state, err := st.GetRoomState(ctx, token, now)
		if err != nil || state == nil {
			b.Fatalf("GetRoomState = %v, %v", state, err)
		}
	}
}

// BenchmarkSubmitBotReply measures an accepted atomic turn claim + reply,
// alternating between two online members so every iteration wins the CAS.
func BenchmarkSubmitBotReply(b *testing.B) {
	const limit = int64(1 << 20)
	previous := maxTurnsLimit
	SetMaxTurnsLimit(limit)
	b.Cleanup(func() { SetMaxTurnsLimit(previous) })

	st := openBenchStore(b)
	now := time.Now().UnixMilli()
	id := createBenchGroup(b, st, now, 2, limit)
	d, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		b.Fatal(err)
	}
	tokens := [2]string{tokenOf(d, "bot_1"), tokenOf(d, "bot_2")}
	for _, m := range d.Members {
		markBenchOnline(b, st, m.AgentToken, now)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		accepted, err := st.SubmitBotReply(ctx, tokens[i%2], int64(i+1), "bench reply", now)
		if err != nil || !accepted {
			b.Fatalf("iteration %d: accepted=%v err=%v", i, accepted, err)
		}
	}
}

// BenchmarkSubmitBotReplyParallel measures the serialized stale-CAS path
// that duplicate bot submits hit under concurrent load.
func BenchmarkSubmitBotReplyParallel(b *testing.B) {
	st := openBenchStore(b)
	now := time.Now().UnixMilli()
	id := createBenchGroup(b, st, now, 2, 500)
	d, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		b.Fatal(err)
	}
	token := tokenOf(d, "bot_1")
	// Accept one reply so turnSeq moves past 1 and every later submit is stale.
	if accepted, err := st.SubmitBotReply(context.Background(), token, 1, "advance", now); err != nil || !accepted {
		b.Fatalf("advance: accepted=%v err=%v", accepted, err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			accepted, err := st.SubmitBotReply(ctx, token, 1, "stale", now)
			if err != nil {
				b.Error(err)
				return
			}
			if accepted {
				b.Error("stale submit was accepted")
				return
			}
		}
	})
}
