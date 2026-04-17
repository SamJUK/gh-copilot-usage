package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const version = "0.2.0"

// ─── Event structures ────────────────────────────────────────────────────────

type RawEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	Timestamp string          `json:"timestamp"`
}

type ModelChangeData struct {
	NewModel string `json:"newModel"`
}

type AssistantMessageData struct {
	OutputTokens *int `json:"outputTokens"`
}

type ShutdownModelUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

type ShutdownModelRequests struct {
	Count int `json:"count"`
	Cost  int `json:"cost"`
}

type ShutdownModelEntry struct {
	Usage    ShutdownModelUsage    `json:"usage"`
	Requests ShutdownModelRequests `json:"requests"`
}

type ShutdownCodeChanges struct {
	LinesAdded   int `json:"linesAdded"`
	LinesRemoved int `json:"linesRemoved"`
}

type ShutdownData struct {
	TotalPremiumRequests int                           `json:"totalPremiumRequests"`
	ModelMetrics         map[string]ShutdownModelEntry `json:"modelMetrics"`
	CodeChanges          ShutdownCodeChanges           `json:"codeChanges"`
}

// ─── Session record ───────────────────────────────────────────────────────────

type ModelMetrics struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	Requests         int
	PremiumRequests  int
}

type SessionRecord struct {
	Time         time.Time
	Models       map[string]ModelMetrics
	LinesAdded   int
	LinesRemoved int
	FromShutdown bool
}

// ─── Aggregation types ────────────────────────────────────────────────────────

type PeriodData struct {
	Label            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	Requests         int
	PremiumRequests  int
	ByModel          map[string]*ModelMetrics
	HasPartialData   bool // true if any session in this period used fallback (output tokens only)
}

// ─── Parser ──────────────────────────────────────────────────────────────────

func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	return t, err
}

func parseSessionDir(dir string) (*SessionRecord, error) {
	eventsFile := filepath.Join(dir, "events.jsonl")
	f, err := os.Open(eventsFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type modelSwitch struct {
		ts    time.Time
		model string
	}
	var modelSwitches []modelSwitch

	type pendingMsg struct {
		ts     time.Time
		tokens int64
	}
	var pendingMsgs []pendingMsg

	var shutdownRecord *SessionRecord
	var firstTimestamp time.Time

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev RawEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ts, err := parseTimestamp(ev.Timestamp)
		if err != nil {
			continue
		}
		if firstTimestamp.IsZero() {
			firstTimestamp = ts
		}

		switch ev.Type {
		case "session.shutdown":
			var d ShutdownData
			if err := json.Unmarshal(ev.Data, &d); err != nil || len(d.ModelMetrics) == 0 {
				continue
			}
			rec := &SessionRecord{
				Time:         ts,
				Models:       make(map[string]ModelMetrics, len(d.ModelMetrics)),
				LinesAdded:   d.CodeChanges.LinesAdded,
				LinesRemoved: d.CodeChanges.LinesRemoved,
				FromShutdown: true,
			}
			for modelName, entry := range d.ModelMetrics {
				rec.Models[modelName] = ModelMetrics{
					InputTokens:      entry.Usage.InputTokens,
					OutputTokens:     entry.Usage.OutputTokens,
					CacheReadTokens:  entry.Usage.CacheReadTokens,
					CacheWriteTokens: entry.Usage.CacheWriteTokens,
					ReasoningTokens:  entry.Usage.ReasoningTokens,
					Requests:         entry.Requests.Count,
					PremiumRequests:  entry.Requests.Cost,
				}
			}
			shutdownRecord = rec

		case "session.model_change":
			var d ModelChangeData
			if err := json.Unmarshal(ev.Data, &d); err == nil && d.NewModel != "" {
				modelSwitches = append(modelSwitches, modelSwitch{ts: ts, model: d.NewModel})
			}

		case "assistant.message":
			var d AssistantMessageData
			if err := json.Unmarshal(ev.Data, &d); err == nil && d.OutputTokens != nil && *d.OutputTokens > 0 {
				pendingMsgs = append(pendingMsgs, pendingMsg{ts: ts, tokens: int64(*d.OutputTokens)})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Primary: use shutdown event if available (richest data source).
	// Use firstTimestamp (session start) not the shutdown event's timestamp,
	// so multi-day sessions are attributed to the day they began, not ended.
	if shutdownRecord != nil {
		if !firstTimestamp.IsZero() {
			shutdownRecord.Time = firstTimestamp
		}
		return shutdownRecord, nil
	}

	// Fallback: aggregate assistant.message output tokens
	if len(pendingMsgs) == 0 {
		return nil, nil
	}

	sort.Slice(modelSwitches, func(i, j int) bool {
		return modelSwitches[i].ts.Before(modelSwitches[j].ts)
	})

	modelTotals := make(map[string]int64)
	for _, pm := range pendingMsgs {
		model := "unknown"
		for _, ms := range modelSwitches {
			if !ms.ts.After(pm.ts) {
				model = ms.model
			} else {
				break
			}
		}
		modelTotals[model] += pm.tokens
	}

	rec := &SessionRecord{
		Time:         firstTimestamp,
		Models:       make(map[string]ModelMetrics, len(modelTotals)),
		FromShutdown: false,
	}
	for modelName, total := range modelTotals {
		rec.Models[modelName] = ModelMetrics{OutputTokens: total}
	}
	return rec, nil
}

func parseAllSessions(copilotDir string) ([]*SessionRecord, error) {
	sessionStateDir := filepath.Join(copilotDir, "session-state")
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		return nil, fmt.Errorf("cannot read session-state dir: %w", err)
	}

	var all []*SessionRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionStateDir, e.Name())
		rec, err := parseSessionDir(sessionDir)
		if err != nil || rec == nil {
			continue
		}
		all = append(all, rec)
	}
	return all, nil
}

// ─── Aggregation ─────────────────────────────────────────────────────────────

func periodKey(t time.Time, period string) string {
	switch period {
	case "week":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	case "month":
		return t.Format("2006-01")
	default:
		return t.Format("2006-01-02")
	}
}

func periodLabel(key string, period string) string {
	switch period {
	case "week":
		parts := strings.SplitN(key, "-W", 2)
		if len(parts) == 2 {
			return "Week " + parts[1] + " '" + parts[0][2:]
		}
		return key
	case "month":
		t, err := time.Parse("2006-01", key)
		if err == nil {
			return t.Format("Jan 2006")
		}
		return key
	default:
		t, err := time.Parse("2006-01-02", key)
		if err == nil {
			return t.Format("02 Jan")
		}
		return key
	}
}

func aggregate(records []*SessionRecord, period string, days int, splitByModel bool) []PeriodData {
	return aggregateAt(records, period, days, splitByModel, time.Now())
}

func aggregateAt(records []*SessionRecord, period string, days int, splitByModel bool, now time.Time) []PeriodData {
	type bucket struct {
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
		reasoning  int64
		requests   int
		premium    int
		byModel    map[string]*ModelMetrics
		hasPartial bool
	}
	buckets := map[string]*bucket{}
	cutoff := now.AddDate(0, 0, -days)

	for _, r := range records {
		if period == "day" && r.Time.Before(cutoff) {
			continue
		}
		key := periodKey(r.Time, period)
		if _, ok := buckets[key]; !ok {
			buckets[key] = &bucket{byModel: map[string]*ModelMetrics{}}
		}
		b := buckets[key]
		if !r.FromShutdown {
			b.hasPartial = true
		}
		for modelName, mm := range r.Models {
			b.input += mm.InputTokens
			b.output += mm.OutputTokens
			b.cacheRead += mm.CacheReadTokens
			b.cacheWrite += mm.CacheWriteTokens
			b.reasoning += mm.ReasoningTokens
			b.requests += mm.Requests
			b.premium += mm.PremiumRequests
			if splitByModel {
				if _, ok := b.byModel[modelName]; !ok {
					b.byModel[modelName] = &ModelMetrics{}
				}
				bm := b.byModel[modelName]
				bm.InputTokens += mm.InputTokens
				bm.OutputTokens += mm.OutputTokens
				bm.CacheReadTokens += mm.CacheReadTokens
				bm.CacheWriteTokens += mm.CacheWriteTokens
				bm.ReasoningTokens += mm.ReasoningTokens
				bm.Requests += mm.Requests
				bm.PremiumRequests += mm.PremiumRequests
			}
		}
	}

	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]PeriodData, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		result = append(result, PeriodData{
			Label:            periodLabel(k, period),
			InputTokens:      b.input,
			OutputTokens:     b.output,
			CacheReadTokens:  b.cacheRead,
			CacheWriteTokens: b.cacheWrite,
			ReasoningTokens:  b.reasoning,
			Requests:         b.requests,
			PremiumRequests:  b.premium,
			ByModel:          b.byModel,
			HasPartialData:   b.hasPartial,
		})
	}
	return result
}

func sortedModels(data []PeriodData) []string {
	totals := map[string]int64{}
	for _, pd := range data {
		for m, mm := range pd.ByModel {
			totals[m] += mm.OutputTokens
		}
	}
	models := make([]string, 0, len(totals))
	for m := range totals {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		return totals[models[i]] > totals[models[j]]
	})
	return models
}

func aggregateByModel(records []*SessionRecord) map[string]*ModelMetrics {
	result := map[string]*ModelMetrics{}
	for _, r := range records {
		for modelName, mm := range r.Models {
			if _, ok := result[modelName]; !ok {
				result[modelName] = &ModelMetrics{}
			}
			bm := result[modelName]
			bm.InputTokens += mm.InputTokens
			bm.OutputTokens += mm.OutputTokens
			bm.CacheReadTokens += mm.CacheReadTokens
			bm.CacheWriteTokens += mm.CacheWriteTokens
			bm.ReasoningTokens += mm.ReasoningTokens
			bm.Requests += mm.Requests
			bm.PremiumRequests += mm.PremiumRequests
		}
	}
	return result
}

// ─── Styles ──────────────────────────────────────────────────────────────────

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	numberStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	totalStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	boxStyle    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
)

var modelColors = []lipgloss.Color{
	"86",  // cyan
	"212", // pink
	"220", // yellow
	"39",  // blue
	"154", // green
	"208", // orange
}

func modelColor(model string, modelList []string) lipgloss.Color {
	if len(modelList) == 0 {
		return "250"
	}
	for i, m := range modelList {
		if m == model {
			return modelColors[i%len(modelColors)]
		}
	}
	return "250"
}

var barChars = []string{"█", "▓", "▒", "░", "▉", "▊"}

const maxBarWidth = 50

// ─── Formatters ───────────────────────────────────────────────────────────────

func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func shortModelName(model string) string {
	model = strings.TrimPrefix(model, "claude-")
	if len(model) > 12 {
		model = model[:12]
	}
	return model
}

// ─── Renderers ────────────────────────────────────────────────────────────────

func renderSummaryHeader(data []PeriodData, models []string, period string, days int, partialCount int) {
	var totalIn, totalOut, totalCR, totalCW int64
	var totalReq, totalPrem int
	for _, pd := range data {
		totalIn += pd.InputTokens
		totalOut += pd.OutputTokens
		totalCR += pd.CacheReadTokens
		totalCW += pd.CacheWriteTokens
		totalReq += pd.Requests
		totalPrem += pd.PremiumRequests
	}

	var periodDisplay string
	switch period {
	case "week":
		periodDisplay = "Weekly view"
	case "month":
		periodDisplay = "Monthly view"
	default:
		periodDisplay = fmt.Sprintf("Last %d days", days)
	}

	modelCount := len(models)
	if modelCount == 0 {
		modelCount = 1
	}

	line1 := headerStyle.Render("Copilot Usage") +
		dimStyle.Render("  ·  ") +
		labelStyle.Render(periodDisplay) +
		dimStyle.Render("  ·  ") +
		labelStyle.Render(fmt.Sprintf("%d model(s)", modelCount))

	line2 := dimStyle.Render("In: ") + numberStyle.Render(formatTokens(totalIn)) +
		dimStyle.Render("  Out: ") + numberStyle.Render(formatTokens(totalOut)) +
		dimStyle.Render("  Cache: ") + numberStyle.Render(formatTokens(totalCR+totalCW)) +
		dimStyle.Render("  Req: ") + numberStyle.Render(fmt.Sprintf("%d", totalReq)) +
		dimStyle.Render("  Premium: ") + numberStyle.Render(fmt.Sprintf("%d", totalPrem))

	content := line1 + "\n" + line2
	if partialCount > 0 {
		content += "\n" + warnStyle.Render(fmt.Sprintf("⚠  %d session(s) incomplete (crashed/killed) — output tokens only", partialCount))
	}
	fmt.Println(boxStyle.Render(content))
}

func buildModelBar(pd PeriodData, models []string, totalBarLen int) string {
	if pd.OutputTokens == 0 || totalBarLen == 0 {
		return ""
	}
	var sb strings.Builder
	remaining := totalBarLen
	for i, m := range models {
		mm, ok := pd.ByModel[m]
		if !ok || mm.OutputTokens == 0 {
			continue
		}
		chars := int(float64(mm.OutputTokens) / float64(pd.OutputTokens) * float64(totalBarLen))
		if i == len(models)-1 {
			chars = remaining
		}
		if chars <= 0 {
			continue
		}
		bc := barChars[i%len(barChars)]
		sb.WriteString(lipgloss.NewStyle().Foreground(modelColor(m, models)).Render(strings.Repeat(bc, chars)))
		remaining -= chars
	}
	return sb.String()
}

// buildTokenTypeBar renders a stacked bar: output (█) / cache-read (░) / input (▒)
func buildTokenTypeBar(pd PeriodData, totalBarLen int) string {
	total := pd.InputTokens + pd.OutputTokens + pd.CacheReadTokens
	if total == 0 || totalBarLen == 0 {
		return ""
	}
	outLen := int(float64(pd.OutputTokens) / float64(total) * float64(totalBarLen))
	cacheLen := int(float64(pd.CacheReadTokens) / float64(total) * float64(totalBarLen))
	inLen := totalBarLen - outLen - cacheLen
	if inLen < 0 {
		inLen = 0
	}
	var sb strings.Builder
	if outLen > 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Render(strings.Repeat("█", outLen)))
	}
	if cacheLen > 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("░", cacheLen)))
	}
	if inLen > 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render(strings.Repeat("▒", inLen)))
	}
	return sb.String()
}

func renderGraph(data []PeriodData, splitByModel bool, period string) {
	if len(data) == 0 {
		fmt.Println(dimStyle.Render("No data to display."))
		return
	}

	var maxVal int64
	for _, pd := range data {
		var v int64
		if splitByModel {
			v = pd.OutputTokens
		} else {
			v = pd.InputTokens + pd.OutputTokens + pd.CacheReadTokens
		}
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		return
	}

	models := sortedModels(data)

	labelWidth := 8
	for _, pd := range data {
		if len(pd.Label) > labelWidth {
			labelWidth = len(pd.Label)
		}
	}
	labelFmt := fmt.Sprintf("%%-%ds", labelWidth)

	legend := dimStyle.Render("(█ output  ░ cache  ▒ input)")
	title := "Token Usage  " + legend
	if splitByModel {
		title = "Output Tokens by Model"
	}
	fmt.Println()
	fmt.Println(headerStyle.Render("  " + title))
	fmt.Println()

	for _, pd := range data {
		label := labelStyle.Render(fmt.Sprintf(labelFmt, pd.Label))
		var scaleVal int64
		if splitByModel {
			scaleVal = pd.OutputTokens
		} else {
			scaleVal = pd.InputTokens + pd.OutputTokens + pd.CacheReadTokens
		}
		barLen := int(float64(scaleVal) / float64(maxVal) * maxBarWidth)
		if barLen == 0 && scaleVal > 0 {
			barLen = 1
		}

		var bar string
		if splitByModel && len(models) > 1 {
			bar = buildModelBar(pd, models, barLen)
		} else if splitByModel && len(models) == 1 {
			bar = lipgloss.NewStyle().Foreground(modelColor(models[0], models)).Render(strings.Repeat("█", barLen))
		} else {
			bar = buildTokenTypeBar(pd, barLen)
		}

		count := numberStyle.Render(fmt.Sprintf(" %s out", formatTokens(pd.OutputTokens)))
		if pd.HasPartialData {
			count += warnStyle.Render("~")
		}
		fmt.Printf("  %s  %s%s\n", label, bar, count)
	}

	if splitByModel && len(models) > 0 {
		fmt.Println()
		fmt.Print("  Legend: ")
		for i, m := range models {
			bc := barChars[i%len(barChars)]
			colored := lipgloss.NewStyle().Foreground(modelColor(m, models)).Render(bc + " " + shortModelName(m))
			fmt.Print(colored)
			if i < len(models)-1 {
				fmt.Print("  ")
			}
		}
		fmt.Println()
	}
	fmt.Println()
}

func renderLeaderboard(records []*SessionRecord) {
	modelTotals := aggregateByModel(records)
	if len(modelTotals) == 0 {
		return
	}

	models := make([]string, 0, len(modelTotals))
	for m := range modelTotals {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		return modelTotals[models[i]].OutputTokens > modelTotals[models[j]].OutputTokens
	})

	fmt.Println(headerStyle.Render("  Model Leaderboard"))
	fmt.Println()
	fmt.Println(dimStyle.Render(fmt.Sprintf("  %-4s  %-28s  %8s  %8s  %10s  %8s  %7s",
		"Rank", "Model", "Output", "Input", "Cache Read", "Requests", "Premium")))
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 82)))

	for rank, m := range models {
		mm := modelTotals[m]
		row := fmt.Sprintf("  #%-3d  %-28s  %8s  %8s  %10s  %8d  %7d",
			rank+1, m,
			formatTokens(mm.OutputTokens),
			formatTokens(mm.InputTokens),
			formatTokens(mm.CacheReadTokens),
			mm.Requests,
			mm.PremiumRequests,
		)
		if rank == 0 {
			fmt.Println(totalStyle.Render(row))
		} else {
			fmt.Println(labelStyle.Render(row))
		}
	}
	fmt.Println()
}

func renderTable(data []PeriodData, splitByModel bool, models []string) {
	if len(data) == 0 {
		return
	}

	fmt.Println(headerStyle.Render("  Period Breakdown"))
	fmt.Println()

	labelWidth := 8
	for _, pd := range data {
		if len(pd.Label) > labelWidth {
			labelWidth = len(pd.Label)
		}
	}

	if splitByModel && len(models) > 0 {
		hdr := fmt.Sprintf("  %-*s  %-14s  %8s  %8s  %10s  %8s  %7s",
			labelWidth, "Period", "Model", "Output", "Input", "Cache Read", "Requests", "Premium")
		fmt.Println(dimStyle.Render(hdr))
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", labelWidth+68)))

		for _, pd := range data {
			first := true
			for _, m := range models {
				mm, ok := pd.ByModel[m]
				if !ok {
					continue
				}
				label := ""
				if first {
					label = pd.Label
					first = false
				}
				row := fmt.Sprintf("  %-*s  %-14s  %8s  %8s  %10s  %8d  %7d",
					labelWidth, label,
					shortModelName(m),
					formatTokens(mm.OutputTokens),
					formatTokens(mm.InputTokens),
					formatTokens(mm.CacheReadTokens),
					mm.Requests,
					mm.PremiumRequests,
				)
				fmt.Println(labelStyle.Render(row))
			}
		}
	} else {
		hdr := fmt.Sprintf("  %-*s  %8s  %8s  %10s  %10s  %8s  %7s",
			labelWidth, "Period", "Output", "Input", "Cache Read", "Cache Wrt", "Requests", "Premium")
		fmt.Println(dimStyle.Render(hdr))
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", labelWidth+72)))

		var grandOut, grandIn, grandCR, grandCW int64
		var grandReq, grandPrem int
		hasAnyPartial := false

		for _, pd := range data {
			label := pd.Label
			if pd.HasPartialData {
				label += "~"
				hasAnyPartial = true
			}
			row := fmt.Sprintf("  %-*s  %8s  %8s  %10s  %10s  %8d  %7d",
				labelWidth, label,
				formatTokens(pd.OutputTokens),
				formatTokens(pd.InputTokens),
				formatTokens(pd.CacheReadTokens),
				formatTokens(pd.CacheWriteTokens),
				pd.Requests,
				pd.PremiumRequests,
			)
			if pd.HasPartialData {
				fmt.Println(warnStyle.Render(row))
			} else {
				fmt.Println(labelStyle.Render(row))
			}
			grandOut += pd.OutputTokens
			grandIn += pd.InputTokens
			grandCR += pd.CacheReadTokens
			grandCW += pd.CacheWriteTokens
			grandReq += pd.Requests
			grandPrem += pd.PremiumRequests
		}

		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", labelWidth+72)))
		totRow := fmt.Sprintf("  %-*s  %8s  %8s  %10s  %10s  %8d  %7d",
			labelWidth, "TOTAL",
			formatTokens(grandOut),
			formatTokens(grandIn),
			formatTokens(grandCR),
			formatTokens(grandCW),
			grandReq,
			grandPrem,
		)
		fmt.Println(totalStyle.Render(totRow))
		if hasAnyPartial {
			fmt.Println(warnStyle.Render("  ~ partial data: session ended without clean shutdown (output tokens only)"))
		}
	}
	fmt.Println()
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	var (
		periodFlag        = flag.String("period", "day", "Aggregation period: day, week, month")
		daysFlag          = flag.Int("days", 30, "Number of days to include (for --period day)")
		modelFlag         = flag.Bool("model", false, "Split output by model in graph")
		noGraphFlag       = flag.Bool("no-graph", false, "Skip bar chart")
		noTableFlag       = flag.Bool("no-table", false, "Skip period table")
		noLeaderboardFlag = flag.Bool("no-leaderboard", false, "Skip model leaderboard")
		copilotDirFlag    = flag.String("copilot-dir", "", "Path to .copilot directory (default: ~/.copilot)")
		versionFlag       = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Printf("gh-copilot-usage v%s\n", version)
		return
	}

	copilotDir := *copilotDirFlag
	if copilotDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error: cannot determine home directory:", err)
			os.Exit(1)
		}
		copilotDir = filepath.Join(home, ".copilot")
	}

	period := strings.ToLower(*periodFlag)
	if period != "day" && period != "week" && period != "month" {
		fmt.Fprintln(os.Stderr, "Error: --period must be day, week, or month")
		os.Exit(1)
	}

	days := *daysFlag
	if period == "week" && days < 7 {
		days = 7 * 12
	} else if period == "month" {
		days = 365
	}

	fmt.Printf("\n%s  Parsing sessions from %s…\n",
		dimStyle.Render("◆"),
		accentStyle.Render(copilotDir))

	records, err := parseAllSessions(copilotDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	if len(records) == 0 {
		fmt.Println(dimStyle.Render("  No token data found."))
		return
	}

	fromShutdown := 0
	for _, r := range records {
		if r.FromShutdown {
			fromShutdown++
		}
	}
	partialCount := len(records) - fromShutdown
	fmt.Printf("%s  Found %s sessions (%s with full metrics)\n",
		dimStyle.Render("◆"),
		numberStyle.Render(fmt.Sprintf("%d", len(records))),
		numberStyle.Render(fmt.Sprintf("%d", fromShutdown)))

	data := aggregate(records, period, days, *modelFlag)
	models := sortedModels(data)

	fmt.Println()
	renderSummaryHeader(data, models, period, days, partialCount)

	if !*noGraphFlag {
		renderGraph(data, *modelFlag, period)
	}
	if !*noLeaderboardFlag {
		renderLeaderboard(records)
	}
	if !*noTableFlag {
		renderTable(data, *modelFlag, models)
	}
}
