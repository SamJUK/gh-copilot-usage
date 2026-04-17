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
		input int64
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
		// name longer than 12 chars after stripping prefix
		{"claude-very-long-model-name", "very-long-mo"},
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
		{Label: "d1", ByModel: map[string]*ModelMetrics{
			"model-a": {OutputTokens: 30},
			"model-b": {OutputTokens: 70},
		}},
		{Label: "d2", ByModel: map[string]*ModelMetrics{
			"model-a": {OutputTokens: 150},
			"model-c": {OutputTokens: 50},
		}},
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
		{Label: "d1", ByModel: map[string]*ModelMetrics{}},
	}
	got := sortedModels(data)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// ─── aggregate ───────────────────────────────────────────────────────────────

func makeSessionAt(ts time.Time, outputTokens int64, model string) *SessionRecord {
	return &SessionRecord{
		Time: ts,
		Models: map[string]ModelMetrics{
			model: {OutputTokens: outputTokens},
		},
	}
}

func makeSessionDaysAgo(daysAgo int, outputTokens int64, model string) *SessionRecord {
	return makeSessionAt(time.Now().AddDate(0, 0, -daysAgo), outputTokens, model)
}

func TestAggregateDay(t *testing.T) {
	records := []*SessionRecord{
		makeSessionDaysAgo(0, 100, "model-a"),  // today
		makeSessionDaysAgo(1, 200, "model-a"),  // yesterday
		makeSessionDaysAgo(29, 50, "model-b"),  // 29 days ago — within 30-day window
		makeSessionDaysAgo(31, 999, "model-b"), // 31 days ago — outside window
	}
	data := aggregateAt(records, "day", 30, false, time.Now())
	// Should have 3 days (today, yesterday, 29 days ago), not the 31-days-ago record
	var totalTokens int64
	for _, pd := range data {
		totalTokens += pd.OutputTokens
	}
	if totalTokens != 350 {
		t.Errorf("day aggregate total = %d, want 350", totalTokens)
	}
}

func TestAggregateDayCutoffExclusion(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		makeSessionAt(now.AddDate(0, 0, -5), 100, "m"),  // within window
		makeSessionAt(now.AddDate(0, 0, -15), 200, "m"), // within window
		makeSessionAt(now.AddDate(0, 0, -35), 999, "m"), // outside 30-day window
	}
	data := aggregateAt(records, "day", 30, false, now)
	var total int64
	for _, pd := range data {
		total += pd.OutputTokens
	}
	if total != 300 {
		t.Errorf("expected 300 tokens (filtered), got %d", total)
	}
}

func TestAggregateWeekGrouping(t *testing.T) {
	// Two records in same week, one in a different week
	base := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC) // Monday week 16
	records := []*SessionRecord{
		makeSessionAt(base, 100, "m"),                          // Mon week 16
		makeSessionAt(base.AddDate(0, 0, 4), 200, "m"),         // Fri week 16
		makeSessionAt(base.AddDate(0, 0, 7), 50, "m"),          // Mon week 17
	}
	data := aggregateAt(records, "week", 60, false, base.AddDate(0, 0, 14))
	if len(data) != 2 {
		t.Fatalf("expected 2 week buckets, got %d", len(data))
	}
	// First bucket: week 16 = 300 tokens
	if data[0].OutputTokens != 300 {
		t.Errorf("week 16 total = %d, want 300", data[0].OutputTokens)
	}
	// Second bucket: week 17 = 50 tokens
	if data[1].OutputTokens != 50 {
		t.Errorf("week 17 total = %d, want 50", data[1].OutputTokens)
	}
}

func TestAggregateMonthGrouping(t *testing.T) {
	records := []*SessionRecord{
		makeSessionAt(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), 100, "m"),
		makeSessionAt(time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC), 200, "m"),
		makeSessionAt(time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC), 50, "m"),
	}
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	data := aggregateAt(records, "month", 365, false, now)
	if len(data) != 2 {
		t.Fatalf("expected 2 month buckets, got %d", len(data))
	}
	if data[0].OutputTokens != 300 {
		t.Errorf("Mar 2026 total = %d, want 300", data[0].OutputTokens)
	}
	if data[1].OutputTokens != 50 {
		t.Errorf("Apr 2026 total = %d, want 50", data[1].OutputTokens)
	}
}

func TestAggregateModelSplit(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		makeSessionAt(now.AddDate(0, 0, -1), 300, "model-a"),
		makeSessionAt(now.AddDate(0, 0, -1), 100, "model-b"),
		makeSessionAt(now.AddDate(0, 0, -2), 500, "model-a"),
	}
	data := aggregateAt(records, "day", 30, true, now)

	// Find the day-1 bucket (should have output=400)
	var day1 *PeriodData
	for i := range data {
		if data[i].OutputTokens == 400 {
			day1 = &data[i]
			break
		}
	}
	if day1 == nil {
		t.Fatal("could not find day-1 bucket with OutputTokens=400")
	}
	if day1.ByModel["model-a"] == nil || day1.ByModel["model-a"].OutputTokens != 300 {
		t.Errorf("model-a = %v, want OutputTokens=300", day1.ByModel["model-a"])
	}
	if day1.ByModel["model-b"] == nil || day1.ByModel["model-b"].OutputTokens != 100 {
		t.Errorf("model-b = %v, want OutputTokens=100", day1.ByModel["model-b"])
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
	records := []*SessionRecord{
		makeSessionAt(time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), 10, "m"),
		makeSessionAt(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), 20, "m"),
		makeSessionAt(time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC), 30, "m"),
	}
	data := aggregateAt(records, "day", 30, false, now)
	for i := 1; i < len(data); i++ {
		if data[i].Label < data[i-1].Label {
			t.Errorf("results not sorted: %q comes after %q", data[i].Label, data[i-1].Label)
		}
	}
}

// ─── aggregateByModel ────────────────────────────────────────────────────────

func TestAggregateByModel(t *testing.T) {
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{
			Time: now,
			Models: map[string]ModelMetrics{
				"model-a": {InputTokens: 100, OutputTokens: 50, Requests: 2},
				"model-b": {InputTokens: 200, OutputTokens: 80, Requests: 3},
			},
		},
		{
			Time: now.AddDate(0, 0, -1),
			Models: map[string]ModelMetrics{
				"model-a": {InputTokens: 50, OutputTokens: 25, Requests: 1},
			},
		},
	}
	totals := aggregateByModel(records)

	if totals["model-a"] == nil {
		t.Fatal("model-a missing from aggregateByModel result")
	}
	if totals["model-a"].InputTokens != 150 {
		t.Errorf("model-a InputTokens = %d, want 150", totals["model-a"].InputTokens)
	}
	if totals["model-a"].OutputTokens != 75 {
		t.Errorf("model-a OutputTokens = %d, want 75", totals["model-a"].OutputTokens)
	}
	if totals["model-a"].Requests != 3 {
		t.Errorf("model-a Requests = %d, want 3", totals["model-a"].Requests)
	}
	if totals["model-b"] == nil {
		t.Fatal("model-b missing from aggregateByModel result")
	}
	if totals["model-b"].OutputTokens != 80 {
		t.Errorf("model-b OutputTokens = %d, want 80", totals["model-b"].OutputTokens)
	}
}

// ─── buildModelBar ────────────────────────────────────────────────────────────

func TestBuildModelBarEmpty(t *testing.T) {
	pd := PeriodData{OutputTokens: 0, ByModel: map[string]*ModelMetrics{}}
	got := buildModelBar(pd, []string{"m"}, 10)
	if stripANSI(got) != "" {
		t.Errorf("expected empty bar for zero total, got %q", got)
	}
}

func TestBuildModelBarZeroLength(t *testing.T) {
	pd := PeriodData{OutputTokens: 100, ByModel: map[string]*ModelMetrics{"m": {OutputTokens: 100}}}
	got := buildModelBar(pd, []string{"m"}, 0)
	if stripANSI(got) != "" {
		t.Errorf("expected empty bar for zero barLen, got %q", got)
	}
}

func TestBuildModelBarSingleModel(t *testing.T) {
	pd := PeriodData{OutputTokens: 100, ByModel: map[string]*ModelMetrics{"model-a": {OutputTokens: 100}}}
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
		OutputTokens: 100,
		ByModel: map[string]*ModelMetrics{
			"model-a": {OutputTokens: 50},
			"model-b": {OutputTokens: 50},
		},
	}
	models := []string{"model-a", "model-b"}
	got := buildModelBar(pd, models, 10)
	plain := stripANSI(got)
	if len([]rune(plain)) != 10 {
		t.Errorf("bar length = %d, want 10", len([]rune(plain)))
	}
}

// ─── buildTokenTypeBar ───────────────────────────────────────────────────────

func TestBuildTokenTypeBarZeroTotal(t *testing.T) {
	pd := PeriodData{}
	got := buildTokenTypeBar(pd, 20)
	if stripANSI(got) != "" {
		t.Errorf("expected empty bar for zero total, got %q", got)
	}
}

func TestBuildTokenTypeBarZeroLength(t *testing.T) {
	pd := PeriodData{OutputTokens: 100}
	got := buildTokenTypeBar(pd, 0)
	if stripANSI(got) != "" {
		t.Errorf("expected empty bar for zero length, got %q", got)
	}
}

func TestBuildTokenTypeBarOutputOnly(t *testing.T) {
	// Only output tokens → all █
	pd := PeriodData{OutputTokens: 100}
	got := buildTokenTypeBar(pd, 10)
	plain := stripANSI(got)
	if plain != strings.Repeat("█", 10) {
		t.Errorf("output-only bar = %q, want 10x █", plain)
	}
}

func TestBuildTokenTypeBarMixedTypes(t *testing.T) {
	// Equal split: 100 out, 100 cache-read, 100 input → total 300 → 10 chars each in a 30-wide bar
	pd := PeriodData{
		OutputTokens:    100,
		CacheReadTokens: 100,
		InputTokens:     100,
	}
	got := buildTokenTypeBar(pd, 30)
	plain := stripANSI(got)
	if len([]rune(plain)) != 30 {
		t.Errorf("mixed bar length = %d, want 30", len([]rune(plain)))
	}
	// Each segment should be 10 chars; check chars appear in expected order
	runes := []rune(plain)
	out := string(runes[0:10])
	cache := string(runes[10:20])
	in := string(runes[20:30])
	if out != strings.Repeat("█", 10) {
		t.Errorf("output segment = %q, want 10x █", out)
	}
	if cache != strings.Repeat("░", 10) {
		t.Errorf("cache segment = %q, want 10x ░", cache)
	}
	if in != strings.Repeat("▒", 10) {
		t.Errorf("input segment = %q, want 10x ▒", in)
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
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil record for empty file, got %+v", rec)
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
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	mm, ok := rec.Models["unknown"]
	if !ok {
		t.Fatalf("expected model 'unknown' in record, got models: %v", rec.Models)
	}
	if mm.OutputTokens != 400 {
		t.Errorf("unknown model OutputTokens = %d, want 400", mm.OutputTokens)
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
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if mm, ok := rec.Models["unknown"]; !ok || mm.OutputTokens != 100 {
		t.Errorf("unknown model: %+v, want OutputTokens=100", rec.Models["unknown"])
	}
	if mm, ok := rec.Models["claude-sonnet-4.6"]; !ok || mm.OutputTokens != 200 {
		t.Errorf("claude-sonnet-4.6 model: %+v, want OutputTokens=200", rec.Models["claude-sonnet-4.6"])
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
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	wantModels := map[string]int64{"model-a": 100, "model-b": 200, "model-c": 300}
	for name, want := range wantModels {
		mm, ok := rec.Models[name]
		if !ok {
			t.Errorf("model %q missing from record", name)
			continue
		}
		if mm.OutputTokens != want {
			t.Errorf("model %q OutputTokens = %d, want %d", name, mm.OutputTokens, want)
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
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.Models["unknown"].OutputTokens != 100 {
		t.Errorf("expected 100 output tokens, got %d", rec.Models["unknown"].OutputTokens)
	}
}

func TestParseSessionDirSkipsMalformedLines(t *testing.T) {
	lines := []string{
		`not valid json`,
		`{"type":"assistant.message","data":{"outputTokens":50},"timestamp":"2026-04-10T10:01:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.Models["unknown"].OutputTokens != 50 {
		t.Errorf("expected 50 output tokens, got %d", rec.Models["unknown"].OutputTokens)
	}
}

func TestParseSessionDirWithShutdown(t *testing.T) {
	lines := []string{
		`{"type":"session.start","data":{"sessionId":"abc"},"timestamp":"2026-04-10T10:00:00.000Z"}`,
		`{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.6":{"usage":{"inputTokens":1000,"outputTokens":500,"cacheReadTokens":2000,"cacheWriteTokens":100,"reasoningTokens":0},"requests":{"count":10,"cost":2}}},"codeChanges":{"linesAdded":50,"linesRemoved":20},"totalPremiumRequests":2},"timestamp":"2026-04-10T10:05:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if !rec.FromShutdown {
		t.Error("expected FromShutdown=true")
	}
	mm, ok := rec.Models["claude-sonnet-4.6"]
	if !ok {
		t.Fatal("expected claude-sonnet-4.6 model in shutdown record")
	}
	if mm.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", mm.InputTokens)
	}
	if mm.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", mm.OutputTokens)
	}
	if mm.CacheReadTokens != 2000 {
		t.Errorf("CacheReadTokens = %d, want 2000", mm.CacheReadTokens)
	}
	if mm.CacheWriteTokens != 100 {
		t.Errorf("CacheWriteTokens = %d, want 100", mm.CacheWriteTokens)
	}
	if mm.Requests != 10 {
		t.Errorf("Requests = %d, want 10", mm.Requests)
	}
	if mm.PremiumRequests != 2 {
		t.Errorf("PremiumRequests = %d, want 2", mm.PremiumRequests)
	}
	if rec.LinesAdded != 50 {
		t.Errorf("LinesAdded = %d, want 50", rec.LinesAdded)
	}
	if rec.LinesRemoved != 20 {
		t.Errorf("LinesRemoved = %d, want 20", rec.LinesRemoved)
	}
}

func TestParseSessionDirShutdownTakesPriority(t *testing.T) {
	// Both shutdown event and assistant.message exist — shutdown should win
	lines := []string{
		`{"type":"assistant.message","data":{"outputTokens":9999},"timestamp":"2026-04-10T10:01:00.000Z"}`,
		`{"type":"session.shutdown","data":{"modelMetrics":{"model-x":{"usage":{"outputTokens":42},"requests":{"count":1,"cost":0}}},"codeChanges":{}},"timestamp":"2026-04-10T10:05:00.000Z"}`,
	}
	dir := writeFixture(t, lines)
	rec, err := parseSessionDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if !rec.FromShutdown {
		t.Error("expected FromShutdown=true when shutdown event present")
	}
	if rec.Models["model-x"].OutputTokens != 42 {
		t.Errorf("expected shutdown data (42 tokens), got %d", rec.Models["model-x"].OutputTokens)
	}
	if _, ok := rec.Models["unknown"]; ok {
		t.Error("expected no 'unknown' model when shutdown data is used")
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
		t.Errorf("expected 2 session records from 2 sessions, got %d", len(records))
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
