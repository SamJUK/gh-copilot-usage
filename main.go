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

const version = "0.1.0"

// ─── Event structures ────────────────────────────────────────────────────────

type RawEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	Timestamp string          `json:"timestamp"`
}

type SessionStartData struct {
	StartTime string `json:"startTime"`
}

type ModelChangeData struct {
	NewModel string `json:"newModel"`
}

type AssistantMessageData struct {
	OutputTokens *int   `json:"outputTokens"`
	Model        string `json:"model"`
	InteractionID string `json:"interactionId"`
}

// ─── Token record ─────────────────────────────────────────────────────────────

type TokenRecord struct {
	Time   time.Time
	Tokens int
	Model  string
}

// ─── Aggregation ─────────────────────────────────────────────────────────────

type PeriodData struct {
	Label   string
	Total   int
	ByModel map[string]int
}

// ─── Parser ──────────────────────────────────────────────────────────────────

func parseSessionDir(dir string) ([]TokenRecord, error) {
	eventsFile := filepath.Join(dir, "events.jsonl")
	f, err := os.Open(eventsFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Build model timeline: map from timestamp → model name
	// We track the "current model" at each point in time.
	type modelSwitch struct {
		ts    time.Time
		model string
	}
	var modelSwitches []modelSwitch
	var records []TokenRecord

	// Two-pass approach: first build model timeline, then assign tokens.
	// For memory efficiency we do it in one pass and fix up after.

	type pendingRecord struct {
		ts    time.Time
		tokens int
	}
	var pending []pendingRecord

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev RawEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, ev.Timestamp)
			if err != nil {
				continue
			}
		}

		switch ev.Type {
		case "session.model_change":
			var d ModelChangeData
			if err := json.Unmarshal(ev.Data, &d); err == nil && d.NewModel != "" {
				modelSwitches = append(modelSwitches, modelSwitch{ts: ts, model: d.NewModel})
			}
		case "assistant.message":
			var d AssistantMessageData
			if err := json.Unmarshal(ev.Data, &d); err == nil && d.OutputTokens != nil && *d.OutputTokens > 0 {
				pending = append(pending, pendingRecord{ts: ts, tokens: *d.OutputTokens})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort model switches by time
	sort.Slice(modelSwitches, func(i, j int) bool {
		return modelSwitches[i].ts.Before(modelSwitches[j].ts)
	})

	// Assign model to each token record
	for _, pr := range pending {
		model := "unknown"
		// Find the last model switch before (or at) this timestamp
		for _, ms := range modelSwitches {
			if !ms.ts.After(pr.ts) {
				model = ms.model
			} else {
				break
			}
		}
		records = append(records, TokenRecord{
			Time:   pr.ts,
			Tokens: pr.tokens,
			Model:  model,
		})
	}

	return records, nil
}

func parseAllSessions(copilotDir string) ([]TokenRecord, error) {
	sessionStateDir := filepath.Join(copilotDir, "session-state")
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		return nil, fmt.Errorf("cannot read session-state dir: %w", err)
	}

	var all []TokenRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionStateDir, e.Name())
		records, err := parseSessionDir(sessionDir)
		if err != nil {
			// Skip sessions we can't read
			continue
		}
		all = append(all, records...)
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
	default: // day
		return t.Format("2006-01-02")
	}
}

func periodLabel(key string, period string) string {
	switch period {
	case "week":
		// key = "2026-W15"
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

func aggregate(records []TokenRecord, period string, days int, splitByModel bool) []PeriodData {
	return aggregateAt(records, period, days, splitByModel, time.Now())
}

func aggregateAt(records []TokenRecord, period string, days int, splitByModel bool, now time.Time) []PeriodData {
	type bucket struct {
		total   int
		byModel map[string]int
	}
	buckets := map[string]*bucket{}

	cutoff := now.AddDate(0, 0, -days)
	if period == "month" {
		cutoff = now.AddDate(0, -days/30, 0)
	}

	for _, r := range records {
		if period == "day" && r.Time.Before(cutoff) {
			continue
		}
		key := periodKey(r.Time, period)
		if _, ok := buckets[key]; !ok {
			buckets[key] = &bucket{byModel: map[string]int{}}
		}
		buckets[key].total += r.Tokens
		if splitByModel {
			buckets[key].byModel[r.Model] += r.Tokens
		}
	}

	// Collect and sort keys
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]PeriodData, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		pd := PeriodData{
			Label:   periodLabel(k, period),
			Total:   b.total,
			ByModel: b.byModel,
		}
		result = append(result, pd)
	}
	return result
}

// ─── Styles ──────────────────────────────────────────────────────────────────

var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	numberStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	totalStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

// Model colours cycle — assigned consistently by sort order
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

// Bar characters: one per model slot (cycles if >4 models)
var barChars = []string{"█", "▓", "▒", "░", "▉", "▊"}

// ─── Renderer ─────────────────────────────────────────────────────────────────

const maxBarWidth = 50

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func sortedModels(data []PeriodData) []string {
	seen := map[string]int{}
	for _, pd := range data {
		for m, t := range pd.ByModel {
			seen[m] += t
		}
	}
	models := make([]string, 0, len(seen))
	for m := range seen {
		models = append(models, m)
	}
	// Sort by total tokens descending so most-used model gets primary color
	sort.Slice(models, func(i, j int) bool {
		return seen[models[i]] > seen[models[j]]
	})
	return models
}

func renderGraph(data []PeriodData, splitByModel bool, period string) {
	if len(data) == 0 {
		fmt.Println(dimStyle.Render("No data to display."))
		return
	}

	// Find max total
	maxVal := 0
	for _, pd := range data {
		if pd.Total > maxVal {
			maxVal = pd.Total
		}
	}
	if maxVal == 0 {
		return
	}

	models := sortedModels(data)

	// Determine label width
	labelWidth := 8
	for _, pd := range data {
		if len(pd.Label) > labelWidth {
			labelWidth = len(pd.Label)
		}
	}
	labelFmt := fmt.Sprintf("%%-%ds", labelWidth)

	fmt.Println()
	fmt.Println(headerStyle.Render("  Output Token Usage"))
	fmt.Println()

	for _, pd := range data {
		label := labelStyle.Render(fmt.Sprintf(labelFmt, pd.Label))
		barLen := int(float64(pd.Total) / float64(maxVal) * maxBarWidth)
		if barLen == 0 && pd.Total > 0 {
			barLen = 1
		}

		var bar string
		if splitByModel && len(models) > 1 {
			// Build a proportionally split bar
			bar = buildModelBar(pd, models, barLen)
		} else {
			color := accentStyle.GetForeground()
			if splitByModel && len(models) == 1 {
				color = modelColor(models[0], models)
			}
			bar = lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", barLen))
		}

		count := numberStyle.Render(fmt.Sprintf(" %s", formatTokens(pd.Total)))
		fmt.Printf("  %s  %s%s\n", label, bar, count)
	}

	// Legend for model split
	if splitByModel && len(models) > 0 {
		fmt.Println()
		fmt.Print("  Legend: ")
		for i, m := range models {
			bc := barChars[i%len(barChars)]
			colored := lipgloss.NewStyle().Foreground(modelColor(m, models)).Render(bc + " " + m)
			fmt.Print(colored)
			if i < len(models)-1 {
				fmt.Print("  ")
			}
		}
		fmt.Println()
	}
	fmt.Println()
}

func buildModelBar(pd PeriodData, models []string, totalBarLen int) string {
	if pd.Total == 0 || totalBarLen == 0 {
		return ""
	}
	var sb strings.Builder
	remaining := totalBarLen
	for i, m := range models {
		t := pd.ByModel[m]
		if t == 0 {
			continue
		}
		chars := int(float64(t) / float64(pd.Total) * float64(totalBarLen))
		if i == len(models)-1 {
			chars = remaining // give remainder to last model
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

func renderTable(data []PeriodData, splitByModel bool, models []string) {
	if len(data) == 0 {
		return
	}

	fmt.Println(headerStyle.Render("  Detailed Breakdown"))
	fmt.Println()

	// Header
	labelWidth := 8
	for _, pd := range data {
		if len(pd.Label) > labelWidth {
			labelWidth = len(pd.Label)
		}
	}

	headerFmt := fmt.Sprintf("  %%-%ds  %%8s", labelWidth)
	if splitByModel && len(models) > 0 {
		hdr := fmt.Sprintf(headerFmt, "Period", "Total")
		for _, m := range models {
			short := shortModelName(m)
			hdr += fmt.Sprintf("  %10s", short)
		}
		fmt.Println(dimStyle.Render(hdr))
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", labelWidth+2+8+len(models)*12)))
	} else {
		fmt.Println(dimStyle.Render(fmt.Sprintf(headerFmt, "Period", "Tokens")))
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", labelWidth+12)))
	}

	grandTotal := 0
	grandByModel := map[string]int{}

	for _, pd := range data {
		row := fmt.Sprintf("  %-*s  %8s", labelWidth, pd.Label, formatTokens(pd.Total))
		if splitByModel && len(models) > 0 {
			for _, m := range models {
				row += fmt.Sprintf("  %10s", formatTokens(pd.ByModel[m]))
			}
		}
		fmt.Println(labelStyle.Render(row))
		grandTotal += pd.Total
		for m, t := range pd.ByModel {
			grandByModel[m] += t
		}
	}

	// Totals row
	sep := "  " + strings.Repeat("─", labelWidth+2+8)
	if splitByModel && len(models) > 0 {
		sep += strings.Repeat("─", len(models)*12)
	}
	fmt.Println(dimStyle.Render(sep))
	totRow := fmt.Sprintf("  %-*s  %8s", labelWidth, "TOTAL", formatTokens(grandTotal))
	if splitByModel && len(models) > 0 {
		for _, m := range models {
			totRow += fmt.Sprintf("  %10s", formatTokens(grandByModel[m]))
		}
	}
	fmt.Println(totalStyle.Render(totRow))
	fmt.Println()
}

func shortModelName(model string) string {
	// e.g. "claude-sonnet-4.6" → "sonnet-4.6"
	// "gpt-5.4" → "gpt-5.4"
	model = strings.TrimPrefix(model, "claude-")
	if len(model) > 10 {
		model = model[:10]
	}
	return model
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	var (
		periodFlag     = flag.String("period", "day", "Aggregation period: day, week, month")
		daysFlag       = flag.Int("days", 30, "Number of days to include (for --period day/week)")
		modelFlag      = flag.Bool("model", false, "Split output by model")
		noGraphFlag    = flag.Bool("no-graph", false, "Skip bar chart, show table only")
		noTableFlag    = flag.Bool("no-table", false, "Skip table, show graph only")
		copilotDirFlag = flag.String("copilot-dir", "", "Path to .copilot directory (default: ~/.copilot)")
		versionFlag    = flag.Bool("version", false, "Print version and exit")
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

	// Adjust days for week/month
	days := *daysFlag
	if period == "week" && days < 7 {
		days = 7 * 12 // default 12 weeks
	} else if period == "month" {
		days = 365 // always show up to 1 year
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

	fmt.Printf("%s  Found %s token records across sessions\n\n",
		dimStyle.Render("◆"),
		numberStyle.Render(fmt.Sprintf("%d", len(records))))

	data := aggregate(records, period, days, *modelFlag)
	models := sortedModels(data)

	if !*noGraphFlag {
		renderGraph(data, *modelFlag, period)
	}
	if !*noTableFlag {
		renderTable(data, *modelFlag, models)
	}

	// Grand total summary
	total := 0
	for _, pd := range data {
		total += pd.Total
	}

	fmt.Printf("  %s  %s output tokens (%s period)\n\n",
		dimStyle.Render("◆"),
		totalStyle.Render(formatTokens(total)),
		period)
}
