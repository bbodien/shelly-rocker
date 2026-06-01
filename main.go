package main

import (
	"bytes"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

//go:embed shelly-rocker.js
var shellyScript string

const scriptName = "rocker"

// Compatible Gen2+ models that support scripting and control lights.
// Keys are lowercase hostname prefixes as reported by the device.
type modelInfo struct {
	display   string
	numInputs int
}

var compatibleModels = map[string]modelInfo{
	"shellyprodm2pm":   {"ShellyPro Dimmer 2PM", 4}, // 2 channels × 2 inputs
	"shellyprodm1pm":   {"ShellyPro Dimmer 1PM", 2},
	"shellyplusdimmer": {"Shelly Plus Dimmer", 2},
	"shellydimmerg3":   {"Shelly Dimmer Gen3", 2},
	"shelly0110dimg3":  {"Shelly 0-10V Dimmer Gen3", 2},
	"shellypro0110pm":  {"ShellyPro 0-10V PM", 2},
	"shellyplus010v":   {"Shelly Plus 0-10V", 2},
}

// inputCFG generates the INPUT_CFG block for the given number of inputs.
// Each pair of inputs maps to one light channel:
//   input N*2+0 → bottom (on/dim up),  input N*2+1 → top (off/dim down)
func inputCFG(numInputs int) string {
	var b strings.Builder
	b.WriteString("let INPUT_CFG = {\n")
	numChannels := numInputs / 2
	for ch := 0; ch < numChannels; ch++ {
		bottom := ch * 2
		top := ch*2 + 1
		fmt.Fprintf(&b, "  \"input:%d\": [%d,  1],  // SW%d bottom -> light %d on  / dim up\n", bottom, ch, bottom+1, ch)
		fmt.Fprintf(&b, "  \"input:%d\": [%d, -1],  // SW%d top    -> light %d off / dim down\n", top, ch, top+1, ch)
	}
	b.WriteString("};\n")
	return b.String()
}

// buildScript replaces the INPUT_CFG block in shellyScript for the given model.
func buildScript(numInputs int) string {
	cfg := inputCFG(numInputs)
	// Replace from "let INPUT_CFG" up to and including the closing "};\n"
	start := strings.Index(shellyScript, "let INPUT_CFG")
	end := strings.Index(shellyScript, "};\n") + len("};\n")
	if start < 0 || end < len("};\n") {
		return shellyScript
	}
	return shellyScript[:start] + cfg + shellyScript[end:]
}

// ── types ─────────────────────────────────────────────────────────────────────

type device struct {
	Model     string
	Name      string
	Last6     string
	IP        string
	NumInputs int
	Selected  bool
}

type deployStatus int

const (
	pending deployStatus = iota
	deploying
	success
	failed
)

type result struct {
	status   deployStatus
	step     string
	errMsg   string
	warnings []string
}

type appState int

const (
	sForm appState = iota
	sLoading
	sSelect
	sDeploy
	sDone
)

// ── messages ──────────────────────────────────────────────────────────────────

type loadedMsg struct {
	devices []device
	err     error
}

// deployProgressMsg is sent for each step of the deploy and once more when done.
type deployProgressMsg struct {
	index    int
	step     string // current step description, empty when done
	done     bool
	err      error
	warnings []string
	next     tea.Cmd // reads the next progress update from the channel
}

// ── credentials (pointer stable across model value copies) ────────────────────

type creds struct {
	host, user, pass, site string
}

// ── model ─────────────────────────────────────────────────────────────────────

type model struct {
	state   appState
	form    *huh.Form
	spin    spinner.Model
	creds   *creds
	devices []device
	cursor  int
	results []result
	err     error
	height  int
}

// ── styles ────────────────────────────────────────────────────────────────────

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	groupStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	okSty      = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	errSty     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func checkbox(on bool) string {
	if on {
		return okSty.Render("[✓]")
	}
	return dimStyle.Render("[ ]")
}

// ── init ──────────────────────────────────────────────────────────────────────

func newModel() model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))

	c := &creds{host: "https://192.168.1.1", site: "default"}
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("UniFi Host").Placeholder("https://192.168.1.1").Value(&c.host).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("required")
				}
				return nil
			}),
		huh.NewInput().Title("Username").Value(&c.user).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("required")
				}
				return nil
			}),
		huh.NewInput().Title("Password").EchoMode(huh.EchoModePassword).Value(&c.pass).
			Validate(func(s string) error {
				if s == "" {
					return fmt.Errorf("required")
				}
				return nil
			}),
		huh.NewInput().Title("Site").Value(&c.site),
	))

	return model{state: sForm, form: f, spin: s, creds: c}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.form.Init(), m.spin.Tick)
}

// ── update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.form = m.form.WithWidth(msg.Width)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.state = sForm
			return m, nil
		}
		m.devices = msg.devices
		m.state = sSelect
		return m, nil

	case deployProgressMsg:
		if !msg.done {
			m.results[msg.index].step = msg.step
			return m, msg.next
		}
		if msg.err != nil {
			m.results[msg.index] = result{status: failed, errMsg: msg.err.Error(), warnings: msg.warnings}
		} else {
			m.results[msg.index] = result{status: success, warnings: msg.warnings}
		}
		for i := msg.index + 1; i < len(m.devices); i++ {
			if m.devices[i].Selected {
				m.results[i] = result{status: deploying}
				return m, deployCmd(m.devices, i)
			}
		}
		m.state = sDone
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.state == sDone && msg.String() == "q" {
			return m, tea.Quit
		}
	}

	switch m.state {
	case sForm:
		return m.updateForm(msg)
	case sSelect:
		return m.updateSelect(msg)
	}
	return m, nil
}

func (m model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f, cmd := m.form.Update(msg)
	m.form = f.(*huh.Form)
	if m.form.State == huh.StateCompleted {
		m.state = sLoading
		return m, tea.Batch(m.spin.Tick, fetchCmd(m.creds))
	}
	return m, cmd
}

func (m model) updateSelect(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.devices)-1 {
			m.cursor++
		}
	case " ":
		m.devices[m.cursor].Selected = !m.devices[m.cursor].Selected
	case "a":
		for i := range m.devices {
			m.devices[i].Selected = true
		}
	case "n":
		for i := range m.devices {
			m.devices[i].Selected = false
		}
	case "enter":
		return startDeploy(m)
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func startDeploy(m model) (tea.Model, tea.Cmd) {
	m.results = make([]result, len(m.devices))
	m.state = sDeploy
	for i, d := range m.devices {
		if d.Selected {
			m.results[i] = result{status: pending}
		}
	}
	for i, d := range m.devices {
		if d.Selected {
			m.results[i] = result{status: deploying}
			return m, deployCmd(m.devices, i)
		}
	}
	m.state = sDone
	return m, nil
}

// ── view ──────────────────────────────────────────────────────────────────────

func (m model) View() string {
	switch m.state {
	case sForm:
		top := titleStyle.Render("Shelly Script Deploy") + "\n\n"
		if m.err != nil {
			top += errSty.Render("Error: "+m.err.Error()) + "\n\n"
		}
		return top + m.form.View()
	case sLoading:
		return titleStyle.Render("Shelly Script Deploy") + "\n\n" +
			m.spin.View() + " Fetching devices from UniFi…\n"
	case sSelect:
		return m.viewSelect()
	case sDeploy, sDone:
		return m.viewDeploy()
	}
	return ""
}

// listRow is a single rendered line in the device list, with an optional
// device index (-1 for headers/spacers) used to track cursor visibility.
type listRow struct {
	text   string
	devIdx int
}

func (m model) buildRows() []listRow {
	order := []string{}
	seen := map[string]bool{}
	groups := map[string][]int{}
	for i, d := range m.devices {
		if !seen[d.Model] {
			seen[d.Model] = true
			order = append(order, d.Model)
		}
		groups[d.Model] = append(groups[d.Model], i)
	}
	sort.Strings(order)

	var rows []listRow
	for _, mod := range order {
		rows = append(rows, listRow{groupStyle.Render(mod), -1})
		for _, idx := range groups[mod] {
			d := m.devices[idx]
			cur := "  "
			if idx == m.cursor {
				cur = cursorSty.Render(">") + " "
			}
			label := d.Name
			if label == "" {
				label = d.Last6
			}
			rows = append(rows, listRow{
				fmt.Sprintf("  %s%s  %-16s  %s", cur, checkbox(d.Selected), d.IP, dimStyle.Render(label)),
				idx,
			})
		}
		rows = append(rows, listRow{"", -1})
	}
	return rows
}

func (m model) viewSelect() string {
	var b strings.Builder
	title := titleStyle.Render("Shelly Script Deploy") + "\n\n"
	footer := dimStyle.Render("↑/↓  move    space  toggle    a  select all    n  none") + "\n"

	sel := 0
	for _, d := range m.devices {
		if d.Selected {
			sel++
		}
	}
	footer += dimStyle.Render(fmt.Sprintf("enter  deploy (%d/%d selected)    q  quit", sel, len(m.devices)))

	if len(m.devices) == 0 {
		b.WriteString(title)
		b.WriteString(dimStyle.Render("No Shelly devices found on this network.") + "\n")
		b.WriteString(dimStyle.Render("Check that the site name is correct and devices are online.") + "\n\n")
		b.WriteString(dimStyle.Render("q  quit") + "\n")
		return b.String()
	}

	rows := m.buildRows()

	// Find the row index of the cursor device
	cursorRow := 0
	for i, r := range rows {
		if r.devIdx == m.cursor {
			cursorRow = i
			break
		}
	}

	// Calculate visible rows (title=2, footer=2)
	const fixedLines = 4
	visibleRows := m.height - fixedLines
	if visibleRows < 1 {
		visibleRows = len(rows)
	}

	// Scroll to keep cursor visible
	topRow := 0
	if cursorRow >= visibleRows {
		topRow = cursorRow - visibleRows + 1
	}

	end := topRow + visibleRows
	if end > len(rows) {
		end = len(rows)
	}

	b.WriteString(title)
	for _, r := range rows[topRow:end] {
		b.WriteString(r.text + "\n")
	}
	b.WriteString("\n" + footer + "\n")
	return b.String()
}

func (m model) viewDeploy() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Shelly Script Deploy") + "\n\n")

	for i, d := range m.devices {
		if !d.Selected {
			continue
		}
		r := m.results[i]
		var icon string
		switch r.status {
		case pending:
			icon = dimStyle.Render(" · ")
		case deploying:
			icon = " " + m.spin.View()
		case success:
			icon = okSty.Render(" ✓ ")
		case failed:
			icon = errSty.Render(" ✗ ")
		}
		label := d.Name
		if label == "" {
			label = d.Model + "-" + d.Last6
		}
		step := ""
		if r.status == deploying && r.step != "" {
			step = "  " + dimStyle.Render(r.step+"…")
		}
		fmt.Fprintf(&b, "  %s  %-16s  %s%s\n", icon, d.IP, dimStyle.Render(label), step)
		if r.status == failed {
			fmt.Fprintf(&b, "       %s\n", errSty.Render(r.errMsg))
		}
		for _, w := range r.warnings {
			fmt.Fprintf(&b, "       %s\n", lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("⚠ "+w))
		}
	}

	if m.state == sDone {
		b.WriteString("\n" + dimStyle.Render("Done.  Press q to quit.") + "\n")
	}
	return b.String()
}

// ── UniFi ─────────────────────────────────────────────────────────────────────

type unifi struct {
	c    *http.Client
	host string
	csrf string
	isOS bool
}

func newUnifi() *unifi {
	jar, _ := cookiejar.New(nil)
	return &unifi{c: &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}}
}

func (u *unifi) login(host, user, pass string) error {
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u.host = strings.TrimRight(host, "/")
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})

	resp, err := u.c.Post(u.host+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		u.isOS = true
		u.csrf = resp.Header.Get("X-Csrf-Token")
		return nil
	}

	resp2, err := u.c.Post(u.host+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		return fmt.Errorf("authentication failed (HTTP %d)", resp2.StatusCode)
	}
	return nil
}

type unifiClient struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
	MAC      string `json:"mac"`
}

func (u *unifi) clients(site string) ([]unifiClient, error) {
	path := "/api/s/" + site + "/stat/sta"
	if u.isOS {
		path = "/proxy/network" + path
	}
	req, _ := http.NewRequest("GET", u.host+path, nil)
	if u.csrf != "" {
		req.Header.Set("X-Csrf-Token", u.csrf)
	}
	resp, err := u.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []unifiClient `json:"data"`
	}
	return out.Data, json.NewDecoder(resp.Body).Decode(&out)
}

func fetchCmd(c *creds) tea.Cmd {
	return func() tea.Msg {
		u := newUnifi()
		if err := u.login(c.host, c.user, c.pass); err != nil {
			return loadedMsg{err: err}
		}
		cls, err := u.clients(c.site)
		if err != nil {
			return loadedMsg{err: err}
		}
		var devices []device
		for _, cl := range cls {
			hn := strings.ToLower(cl.Hostname)
			if !strings.Contains(hn, "shelly") {
				continue
			}
			mac := strings.ToUpper(strings.ReplaceAll(cl.MAC, ":", ""))
			last6 := mac
			if len(last6) > 6 {
				last6 = last6[len(last6)-6:]
			}
			matched := false
			for prefix, info := range compatibleModels {
				if strings.HasPrefix(hn, prefix) {
					devices = append(devices, device{
						Model:     info.display,
						Name:      cl.Name,
						Last6:     last6,
						IP:        cl.IP,
						NumInputs: info.numInputs,
						Selected:  true,
					})
					matched = true
					break
				}
			}
			if !matched {
				// Show unrecognised Shelly devices so the user can see actual hostnames
				devices = append(devices, device{
					Model:    "Unrecognised: " + cl.Hostname,
					Name:     cl.Name,
					Last6:    last6,
					IP:       cl.IP,
					Selected: false,
				})
			}
		}
		sort.Slice(devices, func(i, j int) bool {
			if devices[i].Model != devices[j].Model {
				return devices[i].Model < devices[j].Model
			}
			return devices[i].IP < devices[j].IP
		})
		return loadedMsg{devices: devices}
	}
}

// ── Shelly RPC ────────────────────────────────────────────────────────────────

func rpc(ip, method string, params map[string]any) (map[string]any, error) {
	body, _ := json.Marshal(params)
	resp, err := http.Post("http://"+ip+"/rpc/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if code, ok := out["code"].(float64); ok && code < 0 {
		return nil, fmt.Errorf("%v", out["message"])
	}
	return out, nil
}

func deployCmd(devices []device, idx int) tea.Cmd {
	ch := make(chan deployProgressMsg, 10)
	next := tea.Cmd(func() tea.Msg { return <-ch })

	go func() {
		d := devices[idx]
		script := buildScript(d.NumInputs)

		send := func(step string) {
			ch <- deployProgressMsg{index: idx, step: step, next: next}
		}
		fail := func(err error) {
			ch <- deployProgressMsg{index: idx, done: true, err: err, next: next}
		}

		send("removing old script")
		if res, err := rpc(d.IP, "Script.List", map[string]any{}); err == nil {
			if scripts, ok := res["scripts"].([]any); ok {
				for _, s := range scripts {
					if sm, ok := s.(map[string]any); ok && sm["name"] == scriptName {
						id := int(sm["id"].(float64))
						rpc(d.IP, "Script.Stop", map[string]any{"id": id})
						rpc(d.IP, "Script.Delete", map[string]any{"id": id})
					}
				}
			}
		}

		send("uploading script")
		res, err := rpc(d.IP, "Script.Create", map[string]any{"name": scriptName})
		if err != nil {
			fail(fmt.Errorf("create: %w", err))
			return
		}
		id := int(res["id"].(float64))
		if _, err := rpc(d.IP, "Script.PutCode", map[string]any{
			"id": id, "code": script, "append": false,
		}); err != nil {
			fail(fmt.Errorf("upload: %w", err))
			return
		}

		send("starting script")
		rpc(d.IP, "Script.SetConfig", map[string]any{
			"id":     id,
			"config": map[string]any{"name": scriptName, "enable": true},
		})
		rpc(d.IP, "Script.Start", map[string]any{"id": id})

		send("setting output to detached")
		var warnings []string
		numChannels := d.NumInputs / 2
		for c := 0; c < numChannels; c++ {
			res, err := rpc(d.IP, "Light.SetConfig", map[string]any{
				"id":     c,
				"config": map[string]any{"in_mode": "detached"},
			})
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("detach ch%d: %v", c, err))
			} else if restart, ok := res["restart_required"].(bool); ok && restart {
				warnings = append(warnings, fmt.Sprintf("ch%d: restart required", c))
			}
		}

		ch <- deployProgressMsg{index: idx, done: true, warnings: warnings, next: next}
	}()

	return next
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
