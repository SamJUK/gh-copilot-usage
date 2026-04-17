package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── formatTokens ─────────────────────────────────────────────────────────────

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{999999, "1000.0K"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{10200000, "10.2M"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.input), func(t *testing.T) {
			got := formatTokens(tt.input)
			if got != tt.want {
				t.Errorf("formatTokens(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── shortModelName ──────────────────────────────────────────────────────────

func TestShortModelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4.6", "sonnet-4.6"},
		{"claude-opus-4.6", "opus-4.6"},
		{"claude-haiku-4.5", "haiku-4.5"},
		{"gpt-5.4", "gpt-5.4"},
		{"unknown", "unknown"},
		// name longer than 10 chars after stripping prefix
		{"claude-very-long-model-name", "very-long-"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shortModelName(tt.input)
			if got != tt.want {
				t.Errorf("shortModelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── periodKey ───────────────────────────────────────────────────────────────

func TestPeriodKey(t *testing.T) {
	// 2026-04-17 is a Friday, ISO week 16
	ts := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		period string
		want   string
	}{
		{"day", "2026-04-17"},
		{"week", "2026-W16"},
		{"month", "2026-04"},
	}
	for _, tt := range tests {
		t.Run(tt.period, func(t *testing.T) {
			got := periodKey(ts, tt.period)
			if got != tt.want {
				t.Errorf("periodKey(%s) = %q, want %q", tt.period, got, tt.want)
			}
		})
	}
}

func TestPeriodKeyWeekBoundary(t *testing.T) {
	// First day of a year that starts mid-week: 2026-01-01 is Thursday, week 1
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := periodKey(ts, "week")
	year, week := ts.ISOWeek()
	want := fmt.Sprintf("%d-W%02d", year, week)
	if got != want {
		t.Errorf("periodKey week boundary = %q, want %q", got, want)
	}
}

// ─── periodLabel ─────────────────────────────────────────────────────────────

func TestPeriodLabel(t *testing.T) {
	tests := []struct {
		key    string
		period string
		want   string
	}{
		{"2026-04-17", "day", "17 Apr"},
		{"2026-01-01", "day", "01 Jan"},
		{"2026-W16", "week", "Week 16 '26"},
		{"2026-W01", "week", "Week 01 '26"},
		{"2026-04", "month", "Apr 2026"},
		{"2026-01", "month", "Jan 2026"},
	}
	for _, tt := range tests {
		t.Run(tt.period+"/"+tt.key, func(t *testing.T) {
			got := periodLabel(tt.key, tt.period)
			if got != tt.want {
				t.Errorf("periodLabel(%q, %q) = %q, want %q", tt.key, tt.period, got, tt.want)
			}
		})
	}
}

func TestPeriodLabelInvalidKey(t *testing.T) {
	// Invalid keys should be returned as-is
	got := periodLabel("not-a-date", "day")
	if got != "not-a-date" {
		t.Errorf("expected raw key passthrough, got %q", got)
	}
}

// ─── sortedModels ─────────────────────────────────────────────────────────────

func TestSortedModels(t *testing.T) {
	data := []PeriodData{
		{Label: "d1", Total: 100, ByModel: map[string]int{"model-a": 30, "model-b": 70}},
		{Label: "d2", Total: 200, ByModel: map[string]int{"model-a": 150, "model-c": 50}},
	}
	got := sortedModels(data)
	// model-a: 180, model-b: 70, model-c: 50 → order: a, b, c
	want := []string{"model-a", "model-b", "model-c"}
	if len(got) != len(want) {
		t.Fatalf("sortedModels len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sortedModels[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSortedModelsEmpty(t *testing.T) {
	got := sortedModels(nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestSortedModelsNoByModel(t *testing.T) {
	data := []PeriodData{
		{Label: "d1", Total: 100, ByModel: map[string]int{}},
	}
	got := sortedModels(data)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// ─── aggregate ───────────────────────────────────────────────────────────────

func makeRecord(daysAgo int, tokens int, model string) TokenRecord {
	return TokenRecord{
		Time:   time.Now().AddDate(0, 0, -daysAgo),
		Tokens: tokens,
		Model:  model,
	}
}

func TestAggregateDay(t *testing.T) {
	records := []TokenRecord{
		makeRecord(0, 100, "model-a"), // today
		makeRecord(1, 200, "model-a"), // yesterday
		makeRecord(29, 50, "model-b"), // 29 days ago — within 30-day window
		makeRecord(31, 999, "model-b"), // 31 days ago — outside window
	}
	data := aggregateAt(records, "day", 30, false, time.Now())
	// Should have 3 days (today, yesterday, 29 days ago), not the 31-days-ago record
	totalTokens := 0
	for _, pd := range data {
		totalTokens += pd.Total
	}
	if totalTokens != 350 {
		t.Errorf("day aggregate total = %d, want 350", totalTokens)
	}
}

func TestAggregateDayCutoffExclusion(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	records := []TokenRecord{
		{Time: now.AddDate(0, 0, -5), Tokens: 100, Model: "m"},  // within window
		{Time: now.AddDate(0, 0, -15), Tokens: 200, Model: "m"}, // within window
		{Time: now.AddDate(0, 0, -35), Tokens: 999, Model: "m"}, // outside 30-day window
	}
	data := aggregateAt(records, "day", 30, false, now)
	total := 0
	for _, pd := range data {
		total += pd.Total
	}
	if total != 300 {
		t.Errorf("expected 300 tokens (filtered), got %d", total)
	}
}

func TestAggregateWeekGrouping(t *testing.T) {
	// Two records in same week, one in a different week
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC) // Monday week 16
	records := []TokenRecord{
		{Time: base, Tokens: 100, Model: "m"},                          // Mon week 16
		{Time: base.AddDate(0, 0, 4), Tokens: 200, Model: "m"},         // Fri week 16
		{Time: base.AddDate(0, 0, 7), Tokens: 50, Model: "m"},          // Mon week 17
	}
	data := aggregateAt(records, "week", 60, false, base.AddDate(0, 0, 14))
	if len(data) != 2 {
		t.Fatalf("expected 2 week buckets, got %d", len(data))
	}
	// First bucket: week 16 = 300 tokens
	if data[0].Total != 300 {
		t.Errorf("week 16 total = %d, want 300", data[0].Total)
	}
	// Second bucket: week 17 = 50 tokens
	if data[1].Total != 50 {
		t.Errorf("week 17 total = %d, want 50", data[1].Total)
	}
}

func TestAggregateMonthGrouping(t *testing.T) {
	records := []TokenRecord{
		{Time: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), Tokens: 100, Model: "m"},
		{Time: time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC), Tokens: 200, Model: "m"},
		{Time: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC), Tokens: 50, Model: "m"},
	}
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	data := aggregateAt(records, "month", 365, false, now)
	if len(data) != 2 {
		t.Fatalf("expected 2 month buckets, got %d", len(data))
	}
	if data[0].Total != 300 {
		t.Errorf("Mar 2026 total = %d, want 300", data[0].Total)
	}
	if data[1].Total != 50 {
		t.Errorf("Apr 2026 total = %d, want 50", data[1].Total)
	}
}

func TestAggregateModelSplit(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	records := []TokenRecord{
		{Time: now.AddDate(0, 0, -1), Tokens: 300, Model: "model-a"},
		{Time: now.AddDate(0, 0, -1), Tokens: 100, Model: "model-b"},
		{Time: now.AddDate(0, 0, -2), Tokens: 500, Model: "model-a"},
	}
	data := aggregateAt(records, "day", 30, true, now)

	// Find the day-1 bucket
	var day1 *PeriodData
	for i := range data {
		if data[i].Total == 400 {
			day1 = &data[i]
			break
		}
	}
	if day1 == nil {
		t.Fatal("could not find day-1 bucket with total 400")
	}
	if day1.ByModel["model-a"] != 300 {
		t.Errorf("model-a = %d, want 300", day1.ByModel["model-a"])
	}
	if day1.ByModel["model-b"] != 100 {
		t.Errorf("model-b = %d, want 100", day1.ByModel["model-b"])
	}
}

func TestAggregateEmptyRecords(t *testing.T) {
	data := aggregateAt(nil, "day", 30, false, time.Now())
	if len(data) != 0 {
		t.Errorf("expected empty, got %d items", len(data))
	}
}

func TestAggregateResultsAreSortedByKey(t *testing.T) {
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	records := []TokenRecord{
		{Time: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), Tokens: 10, Model: "m"},
		{Time: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), Tokens: 20, Model: "m"},
		{Time: time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC), Tokens: 30, Model: "m"},
	}
	data := aggregateAt(records, "day", 30, false, now)
	for i := 1; i < len(data); i++ {
		if data[i].Label < data[i-1].Label {
			t.Errorf("results not sorted: %q comes after %q", data[i].Label, data[i-1].Label)
		}
	}
}

// ─── buildModelBar ────────────────────────────────────────────────────────────

func TestBuildModelBarEmpty(t *testing.T) {
	pd := PeriodData{Total: 0, ByModel: map[string]int{}}
	got := buildModelBar(pd, []string{"m"}, 10)
	// strip ANSI for comparison
	if stripANSI(got) != "" {
		t.Errorf("expected empty bar for zero total, got %q", got)
	}
}

func TestBuildModelBarZeroLength(t *testing.T) {
	pd := PeriodData{Total: 100, ByModel: map[string]int{"m": 100}}
	got := buildModelBar(pd, []string{"m"}, 0)
	if stripANSI(got) != "" {
		t.Errorf("expected empty bar for zero barLen, got %q", got)
	}
}

func TestBuildModelBarSingleModel(t *testing.T) {
	pd := PeriodData{Total: 100, ByModel: map[string]int{"model-a": 100}}
	got := buildModelBar(pd, []string{"model-a"}, 10)
	plain := stripANSI(got)
	// All 10 chars should be bar char for model-a (index 0 = "█")
	if plain != strings.Repeat("█", 10) {
		t.Errorf("single model bar = %q, want 10x █", plain)
	}
}

func TestBuildModelBarTwoModels(t *testing.T) {
	// 50/50 split across 10 chars → 5 each
	pd := PeriodData{
		Total:   100,
		ByModel: map[string]int{"model-a": 50, "model-b": 50},
	}
	models := []string{"model-a", "model-b"}
	got := buildModelBar(pd, models, 10)
	plain := stripANSI(got)
	if len([]rune(plain)) != 10 {
		t.Errorf("bar length = %d, want 10", len([]rune(plain)))
	}
}

// ─── parseSessionDir ─────────────────────────────────────────────────────────

func writeFixture(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseSessionDirNoFile(t *testing.T) {
	dir := t.TempDir()
	_, err := parseSessionDir(dir)
	if err == nil {
		t.Error("expected error for missing events.jsonl, got nil")
	}
}

func TestParseSessionDirEmpty(t *testing.T) {
	dir := writeFixture(t, []string{})
	records, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestParseSessionDirTokensNoModel(t *testing.T) {
	// Messages with outputTokens but no model change → model should be "unknown"
	lines := []string{
		`{"type":"session.start","data":{"sessionId":"abc","startTime":"2026-04-10T10:00:00.000Z"},"timestamp":"2026-04-10T10:00:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m1","outputTokens":150},"timestamp":"2026-04-10T10:01:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m2","outputTokens":250},"timestamp":"2026-04-10T10:02:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	records, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	for _, r := range records {
		if r.Model != "unknown" {
			t.Errorf("expected model 'unknown', got %q", r.Model)
		}
	}
	if records[0].Tokens != 150 {
		t.Errorf("record[0].Tokens = %d, want 150", records[0].Tokens)
	}
	if records[1].Tokens != 250 {
		t.Errorf("record[1].Tokens = %d, want 250", records[1].Tokens)
	}
}

func TestParseSessionDirModelAssignment(t *testing.T) {
	// Model change happens between two messages
	lines := []string{
		`{"type":"session.start","data":{"sessionId":"abc","startTime":"2026-04-10T10:00:00.000Z"},"timestamp":"2026-04-10T10:00:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m1","outputTokens":100},"timestamp":"2026-04-10T10:01:00.000Z"}`,
		`{"type":"session.model_change","data":{"newModel":"claude-sonnet-4.6"},"timestamp":"2026-04-10T10:02:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m2","outputTokens":200},"timestamp":"2026-04-10T10:03:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	records, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Model != "unknown" {
		t.Errorf("before model change: got %q, want 'unknown'", records[0].Model)
	}
	if records[1].Model != "claude-sonnet-4.6" {
		t.Errorf("after model change: got %q, want 'claude-sonnet-4.6'", records[1].Model)
	}
}

func TestParseSessionDirMultipleModelChanges(t *testing.T) {
	lines := []string{
		`{"type":"session.model_change","data":{"newModel":"model-a"},"timestamp":"2026-04-10T10:01:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m1","outputTokens":100},"timestamp":"2026-04-10T10:02:00.000Z"}`,
		`{"type":"session.model_change","data":{"newModel":"model-b"},"timestamp":"2026-04-10T10:03:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m2","outputTokens":200},"timestamp":"2026-04-10T10:04:00.000Z"}`,
		`{"type":"session.model_change","data":{"newModel":"model-c"},"timestamp":"2026-04-10T10:05:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m3","outputTokens":300},"timestamp":"2026-04-10T10:06:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	records, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	wantModels := []string{"model-a", "model-b", "model-c"}
	for i, r := range records {
		if r.Model != wantModels[i] {
			t.Errorf("record[%d].Model = %q, want %q", i, r.Model, wantModels[i])
		}
	}
}

func TestParseSessionDirSkipsZeroTokens(t *testing.T) {
	lines := []string{
		`{"type":"assistant.message","data":{"messageId":"m1","outputTokens":0},"timestamp":"2026-04-10T10:01:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m2","outputTokens":100},"timestamp":"2026-04-10T10:02:00.000Z"}`,
		`{"type":"assistant.message","data":{"messageId":"m3"},"timestamp":"2026-04-10T10:03:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	records, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the record with 100 tokens should appear
	if len(records) != 1 {
		t.Errorf("expected 1 record (skipping zeros/nil), got %d", len(records))
	}
}

func TestParseSessionDirSkipsMalformedLines(t *testing.T) {
	lines := []string{
		`not valid json`,
		`{"type":"assistant.message","data":{"outputTokens":50},"timestamp":"2026-04-10T10:01:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	records, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record (skipping malformed), got %d", len(records))
	}
}

// ─── parseAllSessions ────────────────────────────────────────────────────────

func TestParseAllSessionsMissingDir(t *testing.T) {
	_, err := parseAllSessions("/nonexistent/path")
	if err == nil {
		t.Error("expected error for missing copilot dir")
	}
}

func TestParseAllSessionsAggregatesMultiple(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "session-state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create two session directories
	for i, tokens := range []int{100, 200} {
		dir := filepath.Join(stateDir, fmt.Sprintf("session-%d", i))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf(
			`{"type":"assistant.message","data":{"outputTokens":%d},"timestamp":"2026-04-10T10:00:00.000Z"}`+"\n",
			tokens,
		)
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	records, err := parseAllSessions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records from 2 sessions, got %d", len(records))
	}
}

func TestParseAllSessionsSkipFiles(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "session-state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Put a plain file in session-state — should be skipped
	if err := os.WriteFile(filepath.Join(stateDir, "notadir.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	// Valid session dir
	dir := filepath.Join(stateDir, "session-0")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"assistant.message","data":{"outputTokens":42},"timestamp":"2026-04-10T10:00:00.000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	records, err := parseAllSessions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// stripANSI removes ANSI escape sequences for plain-text assertions.
func stripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && r == 'm':
			inEscape = false
		case inEscape:
			// still in escape sequence
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
