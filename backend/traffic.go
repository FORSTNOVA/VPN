package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A busy machine carries thousands of connections and the list only has to
// answer what is using the tunnel, so the heaviest ones are sent to the UI and
// the rest are reported as a count.
const maxConnectionRows = 80

// connectionBodyCap bounds what a single kernel reply may cost, in case a very
// large connection table is being read.
const connectionBodyCap = 4 << 20

// connectionsPath is the kernel's own view of what it is carrying, including the
// totals for its whole lifetime.
const connectionsPath = "/connections"

type kernelTraffic struct {
	DownloadTotal int64              `json:"downloadTotal"`
	UploadTotal   int64              `json:"uploadTotal"`
	Connections   []kernelConnection `json:"connections"`
}

type kernelConnection struct {
	ID          string         `json:"id"`
	Upload      int64          `json:"upload"`
	Download    int64          `json:"download"`
	Start       string         `json:"start"`
	Chains      []string       `json:"chains"`
	Rule        string         `json:"rule"`
	RulePayload string         `json:"rulePayload"`
	Metadata    kernelMetadata `json:"metadata"`
}

type kernelMetadata struct {
	Network         string     `json:"network"`
	Type            string     `json:"type"`
	Host            string     `json:"host"`
	DestinationIP   string     `json:"destinationIP"`
	DestinationPort flexString `json:"destinationPort"`
	Process         string     `json:"process"`
	ProcessPath     string     `json:"processPath"`
}

// flexString reads a value the kernel may send either quoted or bare. The kernel
// binary is chosen by the user, and a decode error on one field would otherwise
// empty the whole page.
type flexString string

func (f *flexString) UnmarshalJSON(body []byte) error {
	text := strings.Trim(strings.TrimSpace(string(body)), `"`)
	if text == "null" {
		text = ""
	}
	*f = flexString(text)
	return nil
}

type connectionRow struct {
	Process    string `json:"process"`
	Target     string `json:"target"`
	Network    string `json:"network"`
	Type       string `json:"type"`
	Rule       string `json:"rule"`
	Chain      string `json:"chain"`
	Upload     int64  `json:"upload"`
	Download   int64  `json:"download"`
	DurationMs int64  `json:"durationMs"`
}

// trafficSample is the point in time a pair of totals was read, which is what
// turns two counter readings into a rate.
type trafficSample struct {
	at   time.Time
	up   int64
	down int64
}

// rateFor turns two cumulative readings into bytes per second. A first reading,
// a counter that went backwards (the kernel was restarted) or a non-advancing
// clock all have to report zero rather than a spike or a negative rate.
func rateFor(previous, current trafficSample) (int64, int64) {
	if previous.at.IsZero() {
		return 0, 0
	}
	elapsed := current.at.Sub(previous.at).Seconds()
	if elapsed <= 0 {
		return 0, 0
	}
	if current.up < previous.up || current.down < previous.down {
		return 0, 0
	}
	return int64(float64(current.up-previous.up) / elapsed),
		int64(float64(current.down-previous.down) / elapsed)
}

// connectionRows flattens what the kernel reports into rows that read well: the
// process that opened the connection, where it is going, the rule and the
// outbound that carried it, and how much it has moved.
func connectionRows(connections []kernelConnection, now time.Time) []connectionRow {
	rows := make([]connectionRow, 0, len(connections))
	for _, connection := range connections {
		row := connectionRow{
			Process:  connectionProcess(connection.Metadata),
			Target:   connectionTarget(connection.Metadata),
			Network:  connection.Metadata.Network,
			Type:     connection.Metadata.Type,
			Rule:     connectionRule(connection.Rule, connection.RulePayload),
			Chain:    connectionChain(connection.Chains),
			Upload:   connection.Upload,
			Download: connection.Download,
		}
		if start, err := time.Parse(time.RFC3339Nano, connection.Start); err == nil {
			if elapsed := now.Sub(start); elapsed > 0 {
				row.DurationMs = elapsed.Milliseconds()
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Upload+rows[i].Download > rows[j].Upload+rows[j].Download
	})
	if len(rows) > maxConnectionRows {
		rows = rows[:maxConnectionRows]
	}
	return rows
}

func connectionProcess(metadata kernelMetadata) string {
	if name := strings.TrimSpace(metadata.Process); name != "" {
		return name
	}
	// The kernel reports a full path when it could not name the process.
	trimmed := strings.TrimSpace(metadata.ProcessPath)
	if trimmed == "" {
		return ""
	}
	name := path.Base(strings.ReplaceAll(trimmed, `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func connectionTarget(metadata kernelMetadata) string {
	host := strings.TrimSpace(metadata.Host)
	if host == "" {
		host = strings.TrimSpace(metadata.DestinationIP)
	}
	if host == "" {
		return ""
	}
	if port := strings.TrimSpace(string(metadata.DestinationPort)); port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}

func connectionRule(rule, payload string) string {
	rule = strings.TrimSpace(rule)
	payload = strings.TrimSpace(payload)
	if rule == "" {
		return ""
	}
	if payload == "" {
		return rule
	}
	return rule + "(" + payload + ")"
}

// connectionChain names the outbound that carried the connection. The kernel
// lists the node first and the group that selected it last, so the first entry
// is the one that says where the traffic actually went — it reads "DIRECT" when
// the connection never entered the tunnel.
func connectionChain(chains []string) string {
	for _, entry := range chains {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// kernelTraffic reads the kernel's connection table. It is separate from
// mihomoRequest because the reply is a large typed structure, not a small map.
func (a *app) kernelTraffic() (kernelTraffic, error) {
	return kernelTrafficAt(a.ctrlPort, a.ctrlSecret)
}

func kernelTrafficAt(ctrlPort int, ctrlSecret string) (kernelTraffic, error) {
	request, err := http.NewRequest(http.MethodGet,
		"http://127.0.0.1:"+strconv.Itoa(ctrlPort)+connectionsPath, nil)
	if err != nil {
		return kernelTraffic{}, err
	}
	request.Header.Set("Authorization", "Bearer "+ctrlSecret)
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return kernelTraffic{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return kernelTraffic{}, fmt.Errorf("connection table returned HTTP %d", response.StatusCode)
	}
	var snapshot kernelTraffic
	if err := json.NewDecoder(io.LimitReader(response.Body, connectionBodyCap)).Decode(&snapshot); err != nil {
		return kernelTraffic{}, err
	}
	return snapshot, nil
}

// traffic reports what the tunnel is carrying right now. Monitoring only means
// something while the kernel runs, so it never starts one of its own.
func (a *app) traffic(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.kernelRunningLocked() {
		writeJSON(w, map[string]any{
			"available": false,
			"reason":    "内核未运行：连接与流量监控只在连接期间有意义",
		})
		return
	}
	snapshot, err := a.kernelTraffic()
	if err != nil {
		// Without a reading there is no baseline either, so the next sample
		// starts over instead of reporting the gap as a burst.
		a.lastTraffic = trafficSample{}
		writeJSON(w, map[string]any{"available": false, "reason": "无法读取内核连接表"})
		return
	}

	now := time.Now()
	current := trafficSample{at: now, up: snapshot.UploadTotal, down: snapshot.DownloadTotal}
	upRate, downRate := rateFor(a.lastTraffic, current)
	a.lastTraffic = current

	rows := connectionRows(snapshot.Connections, now)
	writeJSON(w, map[string]any{
		"available":       true,
		"uploadTotal":     snapshot.UploadTotal,
		"downloadTotal":   snapshot.DownloadTotal,
		"uploadRate":      upRate,
		"downloadRate":    downRate,
		"connectionCount": len(snapshot.Connections),
		"shownCount":      len(rows),
		"connections":     rows,
	})
}
