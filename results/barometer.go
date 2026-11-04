package results

import (
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/librespeed/speedtest-go/database"
	"github.com/librespeed/speedtest-go/database/mongodb"
	log "github.com/sirupsen/logrus"
)

// ── Baromètre (IMP-11) ────────────────────────────────────────────────────────
//
// Situe un résultat par rapport aux autres mesures : « plus rapide que 72 %
// des utilisateurs », puis parmi les clients du même opérateur, et donne le
// débit médian de chaque opérateur.
//
// La comparaison se fait à type de connexion égal : un test en 4G comparé à
// des connexions Wi-Fi par fibre serait toujours perdant, et le chiffre ne
// dirait rien de la qualité du réseau mobile.

const (
	barometerDays     = 90
	barometerMinGroup = 5 // en dessous, une médiane d'opérateur n'a pas de sens
	barometerCacheTTL = 2 * time.Minute
)

type BarometerPosition struct {
	DownloadPct int `json:"downloadPct"` // part des mesures que l'on dépasse
	UploadPct   int `json:"uploadPct"`
	PingPct     int `json:"pingPct"` // part des mesures dont la latence est plus élevée
}

type BarometerOperator struct {
	Name           string  `json:"name"`
	Count          int     `json:"count"`
	MedianDownload float64 `json:"medianDownload"`
	MedianUpload   float64 `json:"medianUpload"`
	MedianPing     float64 `json:"medianPing"`
}

type BarometerResponse struct {
	Network   string                     `json:"network"` // "mobile" ou "wifi"
	Days      int                        `json:"days"`
	Count     int                        `json:"count"`
	You       *BarometerPosition         `json:"you,omitempty"`
	Operator  *BarometerOperatorPosition `json:"operator,omitempty"`
	Operators []BarometerOperator        `json:"operators"`
}

// BarometerOperatorPosition : la position parmi les clients du même opérateur.
type BarometerOperatorPosition struct {
	Name        string `json:"name"`
	Count       int    `json:"count"`
	DownloadPct int    `json:"downloadPct"`
}

// barometerGroup : valeurs triées d'un ensemble de mesures.
type barometerGroup struct {
	down, up, ping []float64
}

func (g *barometerGroup) add(s mongodb.BarometerSample) {
	g.down = append(g.down, s.Download)
	g.up = append(g.up, s.Upload)
	g.ping = append(g.ping, s.Ping)
}

func (g *barometerGroup) sort() {
	sort.Float64s(g.down)
	sort.Float64s(g.up)
	sort.Float64s(g.ping)
}

type barometerSnapshot struct {
	at        time.Time
	all       map[string]*barometerGroup            // réseau -> mesures
	operators map[string]map[string]*barometerGroup // réseau -> opérateur -> mesures
}

var (
	barometerMu    sync.Mutex
	barometerCache *barometerSnapshot
)

// barometerNetwork range une mesure dans « mobile » ou « wifi », ou "" si le
// type de connexion est inconnu.
func barometerNetwork(networkType string) string {
	switch strings.ToLower(strings.TrimSpace(networkType)) {
	case "":
		return ""
	case "wifi", "web", "web connection", "ethernet":
		return "wifi"
	default: // Mobile, 2G, 3G, 4G, 5G
		return "mobile"
	}
}

// barometerOperatorName regroupe les variantes d'un même opérateur : le nom
// vient du registre de l'adresse IP, qui écrit « TELECEL FASO » ou « Telecel
// Faso », « ONATEL (Office National des Telecommunications » tronqué, etc.
func barometerOperatorName(op string) string {
	low := strings.ToLower(op)
	for _, b := range []struct{ key, name string }{
		{"orange", "Orange"},
		{"moov", "Moov Africa"},
		{"telecel", "Telecel Faso"},
		{"onatel", "ONATEL"},
		{"anptic", "ANPTIC"},
		{"canalbox", "Canalbox"},
		{"autorite de regulation", "ARCEP"},
	} {
		if strings.Contains(low, b.key) {
			return b.name
		}
	}
	return strings.TrimSpace(op)
}

func loadBarometer() (*barometerSnapshot, error) {
	barometerMu.Lock()
	defer barometerMu.Unlock()
	if barometerCache != nil && time.Since(barometerCache.at) < barometerCacheTTL {
		return barometerCache, nil
	}
	samples, err := database.MongoDB.FetchBarometerSamples(barometerDays)
	if err != nil {
		return nil, err
	}
	snap := buildBarometer(samples)
	barometerCache = snap
	return snap, nil
}

// buildBarometer range les mesures par type de connexion et par opérateur.
func buildBarometer(samples []mongodb.BarometerSample) *barometerSnapshot {
	snap := &barometerSnapshot{
		at:        time.Now(),
		all:       map[string]*barometerGroup{},
		operators: map[string]map[string]*barometerGroup{},
	}
	for _, s := range samples {
		net := barometerNetwork(s.NetworkType)
		if net == "" {
			continue
		}
		if snap.all[net] == nil {
			snap.all[net] = &barometerGroup{}
			snap.operators[net] = map[string]*barometerGroup{}
		}
		snap.all[net].add(s)
		if name := barometerOperatorName(s.Operator); name != "" {
			ops := snap.operators[net]
			if ops[name] == nil {
				ops[name] = &barometerGroup{}
			}
			ops[name].add(s)
		}
	}
	for net, g := range snap.all {
		g.sort()
		for _, og := range snap.operators[net] {
			og.sort()
		}
	}
	return snap
}

// shareBelow : pourcentage des valeurs strictement inférieures à v, les
// égalités comptant pour moitié — sans quoi deux résultats identiques se
// déclareraient chacun « plus rapide que l'autre ».
func shareBelow(sorted []float64, v float64) int {
	if len(sorted) == 0 {
		return 0
	}
	lo := sort.SearchFloat64s(sorted, v)
	hi := sort.Search(len(sorted), func(i int) bool { return sorted[i] > v })
	return int(math.Round(100 * (float64(lo) + float64(hi-lo)/2) / float64(len(sorted))))
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return round1(sorted[n/2])
	}
	return round1((sorted[n/2-1] + sorted[n/2]) / 2)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// Barometer situe un résultat parmi les autres mesures.
// GET /api/barometer?network=4G&operator=Orange&download=12.3&upload=4.1&ping=38
// Sans download, renvoie seulement les médianes des opérateurs.
func Barometer(w http.ResponseWriter, r *http.Request) {
	if !mongoRequired(w) {
		return
	}
	snap, err := loadBarometer()
	if err != nil {
		log.Errorf("Barometer error: %s", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, answerBarometer(snap, r.URL.Query()))
}

func answerBarometer(snap *barometerSnapshot, q url.Values) BarometerResponse {
	net := barometerNetwork(q.Get("network"))
	if net == "" {
		net = "mobile"
	}
	resp := BarometerResponse{Network: net, Days: barometerDays, Operators: []BarometerOperator{}}
	all := snap.all[net]
	if all == nil {
		return resp
	}
	resp.Count = len(all.down)

	for name, g := range snap.operators[net] {
		if len(g.down) < barometerMinGroup {
			continue
		}
		resp.Operators = append(resp.Operators, BarometerOperator{
			Name: name, Count: len(g.down),
			MedianDownload: median(g.down), MedianUpload: median(g.up), MedianPing: median(g.ping),
		})
	}
	sort.Slice(resp.Operators, func(i, j int) bool {
		return resp.Operators[i].MedianDownload > resp.Operators[j].MedianDownload
	})

	down, err := strconv.ParseFloat(q.Get("download"), 64)
	if err != nil || down < 0 {
		return resp
	}
	up, _ := strconv.ParseFloat(q.Get("upload"), 64)
	pos := &BarometerPosition{
		DownloadPct: shareBelow(all.down, down),
		UploadPct:   shareBelow(all.up, up),
	}
	if ping, err := strconv.ParseFloat(q.Get("ping"), 64); err == nil && ping > 0 {
		// Latence : plus bas est meilleur, on compte celles qui sont pires.
		pos.PingPct = 100 - shareBelow(all.ping, ping)
	}
	resp.You = pos

	name := barometerOperatorName(q.Get("operator"))
	if g := snap.operators[net][name]; name != "" && g != nil && len(g.down) >= barometerMinGroup {
		resp.Operator = &BarometerOperatorPosition{
			Name: name, Count: len(g.down), DownloadPct: shareBelow(g.down, down),
		}
	}
	return resp
}
