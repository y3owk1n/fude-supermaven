// Package main ports supermaven-nvim's binary_handler.lua, textual.lua and
// completion_preview.lua to a fude plugin. Names in comments are the Lua ones.
package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/y3owk1n/fude/api"
	"github.com/y3owk1n/fude/sdk"
)

const (
	hardSizeLimit       = 10e6
	maxStateIDRetention = 50
	suggestionStyle     = "comment"
)

// script runs sm-agent, downloading it first as binary_fetcher.lua does.
const script = `
case "$(uname -s)" in Darwin) platform=macosx;; Linux) platform=linux;; *) echo "supermaven: unsupported os $(uname -s)"; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=aarch64;; x86_64) arch=x86_64;; *) echo "supermaven: unsupported arch $(uname -m)"; exit 1;; esac
dir="${XDG_DATA_HOME:+$XDG_DATA_HOME/supermaven}"
dir="${dir:-$HOME/.supermaven}/binary/v20/$platform-$arch"
bin="$dir/sm-agent"
if [ ! -x "$bin" ]; then
  echo "supermaven: downloading sm-agent to $bin"
  mkdir -p "$dir" || exit 1
  url=$(curl -fsS "https://supermaven.com/api/download-path-v2?platform=$platform&arch=$arch&editor=neovim" | sed -n 's/.*"downloadUrl" *: *"\([^"]*\)".*/\1/p')
  [ -n "$url" ] || { echo "supermaven: unable to find download URL for sm-agent"; exit 1; }
  curl -fsS -o "$bin.tmp" "$url" && chmod 755 "$bin.tmp" && mv "$bin.tmp" "$bin" || { echo "supermaven: sm-agent download failed"; exit 1; }
fi
exec "$bin" stdio
`

type item struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Verify string `json:"verify"`
}

// message follows SM-MESSAGE in the agent's output.
type message struct {
	Kind        string    `json:"kind"`
	StateID     string    `json:"stateId"`
	Items       []item    `json:"items"`
	DustStrings *[]string `json:"dustStrings"`
	ActivateURL string    `json:"activateUrl"`
	Passthrough *message  `json:"passthrough"`
}

// state is the agent's answer to one state update.
type state struct {
	prefix     string
	completion []item
}

type completion struct {
	kind         string // text, jump, delete or skip
	text, dedent string
	isIncomplete bool
}

type update struct {
	Kind    string  `json:"kind"`
	Path    string  `json:"path"`
	Offset  *int    `json:"offset,omitempty"`
	Content *string `json:"content,omitempty"`
}

// sent is the last state sent, reused when nothing changed.
type sent struct {
	path, content string
	offset        int
}

type lastContext struct {
	text, path string
	cursor     int
}

type inlay struct {
	priorDelete    int
	completionText string
	isActive       bool
}

var (
	agent   sdk.Program
	running bool

	stateMap       = map[int]*state{}
	currentStateID int
	lastProvide    time.Time
	wantsPolling   bool
	pollBuffer     int
	changed        = map[string]string{} // path to text, since the last state update
	last           *sent
	dustStrings    []string

	seen *lastContext

	shown   *inlay
	shownID int
)

func init() {
	sdk.Register(api.Registration{Kind: api.Typed, Name: "<Tab>", Help: "accepts supermaven's suggestion"}, func(c, _, _ int) int {
		accept(c)
		return 0
	})

	sdk.On("buffer.changed", func(sdk.Event) { onUpdate() })
	sdk.On("window.moved", func(sdk.Event) { onUpdate() })
	sdk.On("buffer.opened", func(sdk.Event) {
		start()
		onUpdate()
	})
	sdk.On("mode.changed", func(sdk.Event) {
		if sdk.Mode() != "insert" {
			dispose()
		}
	})
	sdk.After(0, func() {
		start()
		onUpdate()
	})
}

func main() {}

func say(msg string) { sdk.Message("[supermaven] " + msg) }

func start() {
	if running {
		return
	}
	last, seen = nil, nil
	wantsPolling = false
	p, err := sdk.Spawn(onLine, "sh", "-c", script)
	if err != nil {
		say("Error starting sm-agent: " + err.Error())
		return
	}
	agent, running = p, true
	sendJSON(map[string]any{"kind": "greeting", "allowGitignore": false})
}

func sendJSON(v any) {
	if !running {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		say("Error encoding a message: " + err.Error())
		return
	}
	if err := agent.Send(string(b) + "\n"); err != nil {
		say("Error writing to sm-agent: " + err.Error())
	}
}

// onLine is process_line.
func onLine(line string, done bool, err error) {
	if done {
		running = false
		dispose()
		if err != nil {
			say("sm-agent exited: " + err.Error())
		}
		return
	}
	text, ok := strings.CutPrefix(line, "SM-MESSAGE ")
	if !ok {
		if msg, ok := strings.CutPrefix(line, "supermaven: "); ok {
			say(msg)
		}
		return
	}
	var m message
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		say("Error reading a message: " + err.Error())
		return
	}
	processMessage(m)
}

func processMessage(m message) {
	switch m.Kind {
	case "response":
		id, _ := strconv.Atoi(m.StateID)
		if s := stateMap[id]; s != nil {
			s.completion = append(s.completion, m.Items...)
		}
		// Stands in for Neovim's 25ms poll.
		if wantsPolling {
			pollOnce()
		}
	case "metadata":
		if m.DustStrings != nil {
			dustStrings = *m.DustStrings
		}
	case "activation_request":
		// :SupermavenUseFree. A request with no URL means the free tier is on.
		if m.ActivateURL != "" {
			sendJSON(map[string]string{"kind": "use_free_version"})
		}
	case "passthrough":
		if m.Passthrough != nil {
			processMessage(*m.Passthrough)
		}
	}
}

// bufferText drops the final newline, as nvim_buf_get_lines does.
func bufferText() (string, error) {
	n, err := sdk.Len()
	if err != nil {
		return "", err
	}
	t, err := sdk.Text(0, n)
	return strings.TrimSuffix(t, "\n"), err
}

func cursor() int {
	c, _ := strconv.Atoi(sdk.About("cursor"))
	return c
}

func onUpdate() {
	if !running {
		return
	}
	b := sdk.Current()
	if b.Path == "" {
		return
	}
	text, err := bufferText()
	if err != nil {
		say("Error reading the buffer: " + err.Error())
		return
	}
	if len(text) > hardSizeLimit {
		say("File is too large to send to server. Skipping...")
		return
	}
	documentChanged(b.Path, text)
	c := min(cursor(), len(text))
	allowed := (seen == nil || text != seen.text) && seen != nil && seen.path == b.Path
	ctx := &lastContext{text: text, path: b.Path, cursor: c}
	if allowed {
		pollBuffer = b.ID
		lastProvide = sdk.Now()
		pollOnce()
	} else if seen == nil || *seen != *ctx {
		dispose()
	}
	seen = ctx
}

func documentChanged(path, text string) {
	changed[path] = text
	sendJSON(map[string]string{"kind": "inform_file_changed", "path": path})
}

func pollOnce() {
	b := sdk.Current()
	if sdk.Now().Sub(lastProvide) > 5*time.Second {
		wantsPolling = false
		return
	}
	wantsPolling = true
	if b.ID != pollBuffer {
		return
	}
	text, err := bufferText()
	if err != nil {
		return
	}
	c := min(cursor(), len(text))
	ls := strings.LastIndexByte(text[:c], '\n') + 1
	le := len(text)
	if i := strings.IndexByte(text[c:], '\n'); i >= 0 {
		le = c + i
	}
	before, after, prefix := text[ls:c], text[c:le], text[:c]
	lines := strings.Split(text, "\n")
	row := strings.Count(prefix, "\n")
	following := func(index int) string {
		if i := row + index; i >= 0 && i < len(lines) {
			return lines[i]
		}
		return ""
	}
	id := submitQuery(b.Path, text, prefix)
	mc := checkState(prefix, before, after, false, following, id)
	if mc == nil {
		dispose()
		return
	}
	switch mc.kind {
	case "jump", "delete", "skip":
		return
	}
	wantsPolling = mc.isIncomplete
	if len(mc.dedent) > 0 && !strings.HasSuffix(before, mc.dedent) {
		return
	}
	for len(mc.dedent) > 0 && len(mc.text) > 0 && mc.dedent[0] == mc.text[0] {
		mc.text, mc.dedent = mc.text[1:], mc.dedent[1:]
	}
	render(c, ls+len(after)+len(before), len(mc.dedent), trimEnd(mc.text), after, before)
}

func submitQuery(path, text, prefix string) int {
	for id := range stateMap {
		if id < currentStateID-maxStateIDRetention {
			delete(stateMap, id)
		}
	}
	now := sent{path: path, content: text, offset: len(prefix)}
	if last != nil && len(changed) == 0 && *last == now {
		return currentStateID
	}
	offset := len(prefix)
	updates := []update{{Kind: "cursor_update", Path: path, Offset: &offset}}
	documentChanged(path, text)
	for p, content := range changed {
		updates = append(updates, update{Kind: "file_update", Path: p, Content: &content})
	}
	changed = map[string]string{}
	currentStateID++
	sendJSON(struct {
		Kind    string   `json:"kind"`
		NewID   string   `json:"newId"`
		Updates []update `json:"updates"`
	}{"state_update", strconv.Itoa(currentStateID), updates})
	stateMap[currentStateID] = &state{prefix: prefix}
	last = &now
	return currentStateID
}

// params is CompletionParams.
type params struct {
	lineBeforeCursor, lineAfterCursor string
	getFollowingLine                  func(int) string
	canShowPartialLine, canRetry      bool
}

func checkState(prefix, before, after string, canRetry bool, following func(int) string, _ int) *completion {
	p := params{before, after, following, true, canRetry}
	if !running {
		start()
	}
	var best []item
	bestLength, bestID := 0, -1
	for id, s := range stateMap {
		if len(prefix) < len(s.prefix) || !strings.HasPrefix(prefix, s.prefix) {
			continue
		}
		remaining, ok := stripPrefix(s.completion, prefix[len(s.prefix):])
		if !ok {
			continue
		}
		n := 0
		for _, it := range remaining {
			if it.Kind == "text" {
				n += len(it.Text)
			}
		}
		if n > bestLength || n == bestLength && id > bestID {
			best, bestLength, bestID = remaining, n, id
		}
	}
	return deriveCompletion(best, p)
}

func stripPrefix(items []item, prefix string) ([]item, bool) {
	var out []item
	for _, it := range items {
		switch it.Kind {
		case "text":
			n := min(len(it.Text), len(prefix))
			if it.Text[:n] != prefix[:n] {
				return nil, false
			}
			text := it.Text[n:]
			prefix = prefix[n:]
			if len(text) > 0 {
				out = append(out, item{Kind: "text", Text: text})
			}
		case "delete":
			out = append(out, it)
		case "dedent":
			if len(prefix) > 0 {
				return nil, false
			}
			out = append(out, it)
		default:
			if len(prefix) == 0 {
				out = append(out, it)
			}
		}
	}
	return out, true
}

// textual.lua, with 0-based indices and -1 for none.

const whitespace = " \t\n\r\v\f"

func isWhitespace(c byte) bool { return strings.IndexByte(whitespace, c) >= 0 }
func trim(s string) string     { return strings.Trim(s, whitespace) }
func trimEnd(s string) string  { return strings.TrimRight(s, whitespace) }
func trimStart(s string) string {
	return strings.TrimLeft(s, whitespace)
}

func firstNonEmptyNewline(s string) int {
	seen := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && seen {
			return i
		} else if !isWhitespace(s[i]) {
			seen = true
		}
	}
	return -1
}

func isAllDust(line string, dust []string) bool {
	for len(line) > 0 {
		n := len(line)
		line = trimStart(line)
		for _, d := range dust {
			if strings.HasPrefix(line, d) {
				line = line[len(d):]
			}
		}
		if len(line) == n {
			return false
		}
	}
	return true
}

func hasLeadingNewline(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return true
		} else if !isWhitespace(s[i]) {
			return false
		}
	}
	return false
}

func canDelete(p params) bool {
	return trim(p.lineBeforeCursor) != "" || isAllDust(p.lineAfterCursor, dustStrings)
}

func finishCompletion(output, dedent string, p params) *completion {
	if !canDelete(p) {
		return nil
	}
	trailing := len(trim(p.lineAfterCursor)) > 0
	if trim(output) == "" {
		return nil
	}
	text := func(t string, incomplete bool) *completion {
		return &completion{kind: "text", text: t, dedent: dedent, isIncomplete: incomplete}
	}
	if hasLeadingNewline(output) {
		first, lastNL := firstNonEmptyNewline(output), strings.LastIndexByte(output, '\n')
		if first >= 0 && lastNL >= 0 {
			return text(output[:lastNL+1], false)
		}
		return nil
	}
	if i := firstNonEmptyNewline(output); i >= 0 {
		return text(output[:i+1], false)
	}
	if p.canRetry {
		return text(output, true)
	}
	if trailing || trim(p.lineBeforeCursor) == "" {
		return nil
	}
	if p.canShowPartialLine {
		return text(output, true)
	}
	return nil
}

func forceComplete(output, dedent string, p params) *completion {
	if c := finishCompletion(output+"\n", dedent, p); c != nil {
		return c
	}
	return &completion{kind: "text"}
}

func deriveCompletion(items []item, p params) *completion {
	output, dedent := "", ""
	var deleteLines []string
	for _, it := range items {
		if it.Kind == "end" {
			if strings.Contains(output, "\n") {
				return forceComplete(output, dedent, p)
			}
			return nil
		}
		if len(deleteLines) > 0 && it.Kind != "delete" {
			return &completion{kind: "delete"}
		}
		switch it.Kind {
		case "text":
			output += it.Text
		case "barrier", "finish_edit":
			if trim(output) != "" {
				return forceComplete(output, dedent, p)
			}
		case "dedent":
			dedent += it.Text
		case "jump":
			if trim(output) != "" {
				return &completion{kind: "jump"}
			}
			goto done
		case "delete":
			if trim(output) != "" {
				return forceComplete(output, dedent, p)
			}
			following := p.getFollowingLine(len(deleteLines))
			if trimEnd(it.Verify) == trimEnd(following) {
				deleteLines = append(deleteLines, following)
			}
		case "skip":
			if trim(output) != "" {
				return forceComplete(output, dedent, p)
			}
			return &completion{kind: "skip"}
		}
	}
done:
	output = trimEnd(output)
	if i := firstNonEmptyNewline(output); i >= 0 {
		output = output[:i+1]
	}
	return finishCompletion(output, dedent, p)
}

// render is render_with_inlay. c is the cursor, le its line's end.
func render(c, le, priorDelete int, text, after, before string) {
	dispose()
	if sdk.Mode() != "insert" {
		return
	}
	first, others, _ := strings.Cut(text, "\n")
	var lines []string
	if strings.Contains(text, "\n") {
		lines = strings.Split(others, "\n")
	}
	floating := len(after) > 0 && !strings.Contains(first, after)
	d := api.Decoration{Style: suggestionStyle, Insert: true}
	switch {
	case floating:
		// The whole line as it would read, after the line's end.
		d.Start, d.End, d.Note = le, le, " "+trimStart(before)+first
		text = first
	case after == "":
		d.Start, d.End, d.Note, d.Lines = c, c, first, lines
	default:
		// Covers the rest of the line, which first contains.
		d.Start, d.End, d.Note, d.Lines, d.Cover = c, le, first, lines, true
	}
	shownID = sdk.ID()
	d.ID = shownID
	if err := sdk.Do(api.Result{Decorate: []api.Decoration{d}}); err != nil {
		say("Error showing the suggestion: " + err.Error())
		return
	}
	shown = &inlay{priorDelete: priorDelete, completionText: text, isActive: active(text, before, first)}
}

func active(text, before, first string) bool {
	return text == "" || !isWhitespace(text[0]) || trim(before) != "" || trim(first) == ""
}

func dispose() {
	if shown == nil {
		return
	}
	sdk.Undecorate(shownID)
	shown = nil
}

// accept is on_accept_suggestion. With no suggestion, <Tab> types a tab.
func accept(c int) {
	s := shown
	if s == nil {
		return
	}
	dispose()
	if !s.isActive {
		return
	}
	line, err := sdk.LineOf(c)
	if err != nil {
		return
	}
	ls, _ := sdk.LineStart(line)
	le, _ := sdk.Len()
	if n, _ := sdk.Lines(); line+1 < n {
		next, _ := sdk.LineStart(line + 1)
		le = next - 1
	}
	sdk.Edit(ls+max(c-ls-s.priorDelete, 0), le, s.completionText)
}
