package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	UID             flexString `json:"uid"`
}

func (m *kernelMetadata) UnmarshalJSON(data []byte) error {
	type wire kernelMetadata
	if err := json.Unmarshal(data, (*wire)(m)); err != nil {
		return err
	}
	m.Network = strings.ToLower(m.Network)
	return nil
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
func rateForGo(previous, current trafficSample) (int64, int64) {
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
	q := trafficProjectionRequest{Op: trafficProjectionRows}
	for _, c := range connections {
		q.Rows = append(q.Rows, trafficProjectionConnection(c, now))
	}
	return runSelectedTrafficProjection(q).Rows
}

func connectionProcess(metadata kernelMetadata) string {
	return runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionProcess, Rows: []trafficProjectionFacts{trafficProjectionMetadata(metadata)}}).Text
}

func connectionTarget(metadata kernelMetadata) string {
	return runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionTarget, Rows: []trafficProjectionFacts{trafficProjectionMetadata(metadata)}}).Text
}

func connectionRule(rule, payload string) string {
	return runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionRule, Rows: []trafficProjectionFacts{{Rule: strings.TrimSpace(rule), Payload: strings.TrimSpace(payload)}}}).Text
}

// connectionChain names the outbound that carried the connection. The kernel
// lists the node first and the group that selected it last, so the first entry
// is the one that says where the traffic actually went — it reads "DIRECT" when
// the connection never entered the tunnel.
func connectionChain(chains []string) string {
	f := trafficProjectionFacts{}
	for _, s := range chains {
		f.Chains = append(f.Chains, strings.TrimSpace(s))
	}
	return runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionChain, Rows: []trafficProjectionFacts{f}}).Text
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
	writeTrafficServiceResult(w, a.runTrafficService(trafficServiceRequest{Op: "current"}, nil))
}

func sortTrafficRowsGo(rows []connectionRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Upload+rows[i].Download > rows[j].Upload+rows[j].Download })
}
