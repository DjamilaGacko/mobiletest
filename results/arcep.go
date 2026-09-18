// Package arcep (fichier) implémente le module « Comparaison ARCEP » de Yélé.
//
// Les seuils officiels (profils D1–D4, zones du rapport, QoS navigation/
// streaming) ne vivent PAS dans le code : ils sont lus depuis
// config/seuils.json, monté en volume dans Docker. Modifier un seuil =
// éditer le JSON + redémarrer le backend + committer le fichier. Voir
// seuil.md à la racine du projet pour la conception complète.
//
// Règle d'honnêteté (cf. seuils.md métier) : la comparaison est SIMPLIFIÉE
// (moyenne vs seuil pour le débit ; valeur du test vs seuil pour la QoS),
// pas la méthode ARCEP officielle (90 % des mesures au-dessus du seuil).
// L'API ne renvoie donc jamais de « conforme » — uniquement des faits
// (auDessus: true/false, dansSeuil: true/false) que les clients affichent
// tels quels.
package results

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	log "github.com/sirupsen/logrus"

	"github.com/librespeed/speedtest-go/database"
)

// ── Structure du fichier config/seuils.json ─────────────────────────────────

type arcepProfile struct {
	DownloadMbps float64 `json:"download_mbps"`
	UploadMbps   float64 `json:"upload_mbps"`
}

type arcepZone struct {
	Profile string   `json:"profile"`
	Aliases []string `json:"aliases,omitempty"`
	Lat     float64  `json:"lat,omitempty"`
	Lng     float64  `json:"lng,omitempty"`
}

// arcepQos porte les seuils QoS nationaux (navigation web / streaming vidéo),
// ajoutés en v3 — cf. seuil.md §9.
type arcepQos struct {
	Navigation struct {
		PageLoadS   float64 `json:"page_load_s"`
		PageLoadPct float64 `json:"page_load_pct"`
		ConnectionS float64 `json:"connection_s"`
		TransferPct float64 `json:"transfer_pct"`
		Source      string  `json:"source"`
	} `json:"navigation"`
	Streaming struct {
		PreloadS   float64 `json:"preload_s"`
		QualityPct float64 `json:"quality_pct"`
	} `json:"streaming"`
	QualiteVideo struct {
		RebufferRatioMax float64 `json:"rebuffer_ratio_max"`
		ResolutionMin    string  `json:"resolution_min"`
		InterruptionsMax int     `json:"interruptions_max"`
	} `json:"qualite_video"`
}

type arcepConfig struct {
	Version            int                     `json:"version"`
	Source             string                  `json:"source"`
	Periode            string                  `json:"periode"`
	MethodNote         string                  `json:"method_note"`
	MinSamplesForBadge int                     `json:"min_samples_for_badge"`
	RadiusKm           float64                 `json:"radius_km"`
	Qos                *arcepQos               `json:"qos,omitempty"`
	Profiles           map[string]arcepProfile `json:"profiles"`
	Zones              map[string]arcepZone    `json:"zones"`
}

// ── État chargé (lecture protégée : le fichier ne change qu'au démarrage,
//    mais le verrou rend un futur rechargement à chaud sans risque) ─────────

var (
	arcepMu      sync.RWMutex
	arcepLoaded  *arcepConfig
	arcepAliases map[string]string // clé normalisée → nom canonique de zone
)

// LoadArcepConfig lit et valide config/seuils.json. Appelé au démarrage du
// backend ; en cas de fichier invalide, le backend démarre quand même (le
// module renverra une erreur propre) et la cause est journalisée.
func LoadArcepConfig(path string) {
	cfg, err := loadArcepFile(path)
	if err != nil {
		log.Errorf("arcep: chargement de %s impossible (%v) — endpoint /api/arcep désactivé", path, err)
		arcepMu.Lock()
		arcepLoaded = nil
		arcepAliases = nil
		arcepMu.Unlock()
		return
	}
	arcepMu.Lock()
	arcepLoaded = cfg
	arcepAliases = buildAliases(cfg)
	arcepMu.Unlock()
	log.Infof("arcep: %d zones chargées depuis %s (config v%d)", len(cfg.Zones), path, cfg.Version)
}

func loadArcepFile(path string) (*arcepConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg arcepConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("JSON invalide: %w", err)
	}
	if len(cfg.Zones) == 0 || len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("zones ou profils vides")
	}
	for name, z := range cfg.Zones {
		if _, ok := cfg.Profiles[z.Profile]; !ok {
			return nil, fmt.Errorf("zone %q: profil inconnu %q", name, z.Profile)
		}
	}
	return &cfg, nil
}

// buildAliases construit la table « ville normalisée → zone canonique ».
func buildAliases(cfg *arcepConfig) map[string]string {
	m := make(map[string]string, len(cfg.Zones)*2)
	for canonical, z := range cfg.Zones {
		m[normalizeCity(canonical)] = canonical
		for _, a := range z.Aliases {
			m[normalizeCity(a)] = canonical
		}
	}
	return m
}

// normalizeCity met en minuscules, retire accents et signes diacritiques,
// et supprime espaces, tirets et ponctuation : « Bobo-Dioulasso »,
// « bobo dioulasso » et « bobodioulasso » donnent la même clé.
func normalizeCity(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Mn, r): // marques combinantes (é → e)
			// ignorées
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
		// espaces, tirets, apostrophes, ponctuation : ignorés
	}
	return b.String()
}

// arcepRound2 limite l'affichage à 2 décimales (convention des agrégations
// du dashboard).
func arcepRound2(f float64) float64 {
	return math.Round(f*100) / 100
}

// resolveZoneByCoords retrouve la zone d'un point GPS : la plus proche dont
// le centre est à moins de cfg.RadiusKm (haversine). "" si aucun.
func resolveZoneByCoords(cfg *arcepConfig, lat, lng float64) string {
	if lat == 0 && lng == 0 {
		return ""
	}
	best := ""
	bestKm := cfg.RadiusKm
	for name, z := range cfg.Zones {
		if z.Lat == 0 && z.Lng == 0 {
			continue
		}
		if km := haversineKm(lat, lng, z.Lat, z.Lng); km < bestKm {
			bestKm = km
			best = name
		}
	}
	return best
}

const earthRadiusKm = 6371.0

// haversineKm : distance orthodromique entre deux points (degrés → km).
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// ── Réponse de l'endpoint de comparaison (dashboard) ─────────────────────────

type arcepDirection struct {
	Moyenne  float64 `json:"moyenne"`
	Seuil    float64 `json:"seuil"`
	AuDessus bool    `json:"auDessus"`
}

type arcepZoneResult struct {
	Zone            string         `json:"zone"`
	Profile         string         `json:"profile"`
	NbMesures       int            `json:"nbMesures"`
	Download        arcepDirection `json:"download"`
	Upload          arcepDirection `json:"upload"`
	DonneesLimitees bool           `json:"donneesLimitees"`
}

type arcepResponse struct {
	Config struct {
		Version int    `json:"version"`
		Source  string `json:"source"`
		Periode string `json:"periode,omitempty"`
	} `json:"config"`
	MethodNote string            `json:"methodNote"`
	Zones      []arcepZoneResult `json:"zones"`
	HorsZone   int               `json:"horsZone"`
}

// ── Réponses de la QoS (navigation web / streaming vidéo, seuils nationaux) ──

// arcepQosNavigation : verdict QoS du test individuel sur la navigation.
type arcepQosNavigation struct {
	PageLoadS       float64 `json:"pageLoadS"`       // chargement moyen de CE test
	PageLoadSeuilS  float64 `json:"pageLoadSeuilS"`  // seuil ARCEP (national)
	DansSeuil       bool    `json:"dansSeuil"`
	ConnectionSeuil float64 `json:"connectionSeuil"` // référence affichée (v2 : mesuré)
	Source          string  `json:"source"`
}

// arcepQosStreaming : verdict QoS du test individuel sur le streaming.
type arcepQosStreaming struct {
	PreloadS      float64 `json:"preloadS"`      // préchargement de CE test
	PreloadSeuilS float64 `json:"preloadSeuilS"` // seuil ARCEP (national)
	DansSeuil     bool    `json:"dansSeuil"`
	// Bonne session : définition Yélé (jamais présentée comme définition ARCEP).
	BonneSession        bool    `json:"bonneSession"`
	RebufferRatio       float64 `json:"rebufferRatio"`
	RebufferRatioMax    float64 `json:"rebufferRatioMax"`
	ResolutionMin       string  `json:"resolutionMin"`
	Interruptions       int     `json:"interruptions"`
	InterruptionsMax    int     `json:"interruptionsMax"`
	QualityPctReference float64 `json:"qualityPctReference"` // taux de campagne ARCEP (référence uniquement)
}

type arcepQosBlock struct {
	Navigation *arcepQosNavigation `json:"navigation,omitempty"`
	Streaming  *arcepQosStreaming  `json:"streaming,omitempty"`
}

// ── Endpoint : comparaison par zone (dashboard) ──────────────────────────────

// ArcepComparison répond à GET /api/arcep/comparison : moyennes de débit par
// zone jointes aux seuils du profil (config/seuils.json). Plusieurs libellés
// de ville (variants ipinfo, alias) peuvent pointer vers une même zone : la
// moyenne de la zone est alors pondérée par le nombre de mesures de chacun.
func ArcepComparison(w http.ResponseWriter, r *http.Request) {
	arcepMu.RLock()
	cfg := arcepLoaded
	aliases := arcepAliases
	arcepMu.RUnlock()

	if cfg == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "arcep config unavailable"})
		return
	}
	if !mongoRequired(w) {
		return
	}

	cityStats, err := database.MongoDB.FetchArcepCityStats()
	if err != nil {
		log.Errorf("ArcepComparison error: %s", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Regroupement des villes de la base vers leur zone canonique.
	zoneCounts := make(map[string]int, len(cfg.Zones))
	zoneSums := make(map[string]struct{ dl, ul float64 }, len(cfg.Zones))
	horsZone := 0
	for _, cs := range cityStats {
		canonical, ok := aliases[normalizeCity(cs.City)]
		if !ok {
			horsZone += cs.Count
			continue
		}
		zoneCounts[canonical] += cs.Count
		s := zoneSums[canonical]
		s.dl += cs.AvgDl * float64(cs.Count)
		s.ul += cs.AvgUl * float64(cs.Count)
		zoneSums[canonical] = s
	}

	resp := arcepResponse{
		MethodNote: cfg.MethodNote,
		Zones:      make([]arcepZoneResult, 0, len(cfg.Zones)),
	}
	resp.Config.Version = cfg.Version
	resp.Config.Source = cfg.Source
	resp.Config.Periode = cfg.Periode
	resp.HorsZone = horsZone

	for canonical, z := range cfg.Zones {
		prof := cfg.Profiles[z.Profile]
		n := zoneCounts[canonical]

		var avgDl, avgUl float64
		if n > 0 {
			s := zoneSums[canonical]
			avgDl = s.dl / float64(n)
			avgUl = s.ul / float64(n)
		}

		resp.Zones = append(resp.Zones, arcepZoneResult{
			Zone:      canonical,
			Profile:   z.Profile,
			NbMesures: n,
			Download: arcepDirection{
				Moyenne:  arcepRound2(avgDl),
				Seuil:    prof.DownloadMbps,
				AuDessus: n > 0 && avgDl > prof.DownloadMbps,
			},
			Upload: arcepDirection{
				Moyenne:  arcepRound2(avgUl),
				Seuil:    prof.UploadMbps,
				AuDessus: n > 0 && avgUl > prof.UploadMbps,
			},
			DonneesLimitees: n > 0 && n < cfg.MinSamplesForBadge,
		})
	}

	// Tri stable : zones avec mesures d'abord (par nombre décroissant),
	// puis par nom — une réponse map itérée au hasard serait pénible à lire.
	sort.Slice(resp.Zones, func(i, j int) bool {
		if resp.Zones[i].NbMesures != resp.Zones[j].NbMesures {
			return resp.Zones[i].NbMesures > resp.Zones[j].NbMesures
		}
		return resp.Zones[i].Zone < resp.Zones[j].Zone
	})

	jsonResponse(w, resp)
}

// ── Comparaison à la volée pour UNE mesure (écran Score de l'app) ────────────

type arcepMeasureResponse struct {
	Config struct {
		Version int    `json:"version"`
		Source  string `json:"source"`
		Periode string `json:"periode,omitempty"`
	} `json:"config"`
	MethodNote      string         `json:"methodNote"`
	Found           bool           `json:"found"`
	Zone            string         `json:"zone,omitempty"`
	Profile         string         `json:"profile,omitempty"`
	NbMesures       int            `json:"nbMesures"`
	AvgDownload     float64        `json:"avgDownload"`
	AvgUpload       float64        `json:"avgUpload"`
	SeuilDownload   float64        `json:"seuilDownload"`
	SeuilUpload     float64        `json:"seuilUpload"`
	DonneesLimitees bool           `json:"donneesLimitees"`
	Qos             arcepQosBlock  `json:"qos"`
}

// resolveZone renvoie la zone d'une requête : par GPS (lat/lng, fiable même
// en Wi-Fi) puis repli sur la ville (format ipinfo « Ville, Pays », chaque
// segment essayé).
func resolveZone(cfg *arcepConfig, aliases map[string]string, r *http.Request) string {
	if latS := r.URL.Query().Get("lat"); latS != "" {
		if lngS := r.URL.Query().Get("lng"); lngS != "" {
			lat, errLat := strconv.ParseFloat(latS, 64)
			lng, errLng := strconv.ParseFloat(lngS, 64)
			if errLat == nil && errLng == nil {
				if z := resolveZoneByCoords(cfg, lat, lng); z != "" {
					return z
				}
			}
		}
	}
	city := strings.TrimSpace(r.URL.Query().Get("city"))
	candidates := append([]string{city}, strings.Split(city, ",")...)
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		if z, ok := aliases[normalizeCity(cand)]; ok {
			return z
		}
	}
	return ""
}

// ArcepMeasure répond à GET /api/arcep/measure : état ARCEP de la zone d'une
// mesure (moyennes, nb de mesures, seuils du profil) + seuils QoS nationaux,
// consommé par l'écran de score de l'application mobile. Le bloc qos est
// renvoyé même quand found=false : les seuils navigation/streaming sont
// nationaux et ne dépendent pas de la zone.
// Aucune valeur de mesure ne transite — le client compare lui-même son
// résultat au seuil renvoyé, qui reste la seule vérité (seuils.json).
func ArcepMeasure(w http.ResponseWriter, r *http.Request) {
	arcepMu.RLock()
	cfg := arcepLoaded
	aliases := arcepAliases
	arcepMu.RUnlock()

	if cfg == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "arcep config unavailable"})
		return
	}
	if !mongoRequired(w) {
		return
	}

	resp := arcepMeasureResponse{}
	resp.Config.Version = cfg.Version
	resp.Config.Source = cfg.Source
	resp.Config.Periode = cfg.Periode
	resp.MethodNote = cfg.MethodNote

	// Bloc QoS : toujours renvoyé (seuils nationaux), verdicts calculés par
	// le client avec les valeurs de SON test.
	if cfg.Qos != nil {
		resp.Qos.Navigation = &arcepQosNavigation{
			PageLoadSeuilS:  cfg.Qos.Navigation.PageLoadS,
			ConnectionSeuil: cfg.Qos.Navigation.ConnectionS,
			Source:          cfg.Qos.Navigation.Source,
		}
		resp.Qos.Streaming = &arcepQosStreaming{
			PreloadSeuilS:       cfg.Qos.Streaming.PreloadS,
			RebufferRatioMax:    cfg.Qos.QualiteVideo.RebufferRatioMax,
			ResolutionMin:       cfg.Qos.QualiteVideo.ResolutionMin,
			InterruptionsMax:    cfg.Qos.QualiteVideo.InterruptionsMax,
			QualityPctReference: cfg.Qos.Streaming.QualityPct,
		}
	}

	zone := resolveZone(cfg, aliases, r)
	if zone == "" {
		jsonResponse(w, resp) // found=false : le bloc débit ne s'affichera pas
		return
	}

	// Agrégation des villes de la base vers la zone cible (moyenne pondérée).
	cityStats, err := database.MongoDB.FetchArcepCityStats()
	if err != nil {
		log.Errorf("ArcepMeasure error: %s", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	n := 0
	var sumDl, sumUl float64
	for _, cs := range cityStats {
		if aliases[normalizeCity(cs.City)] != zone {
			continue
		}
		n += cs.Count
		sumDl += cs.AvgDl * float64(cs.Count)
		sumUl += cs.AvgUl * float64(cs.Count)
	}

	z := cfg.Zones[zone]
	prof := cfg.Profiles[z.Profile]
	resp.Found = true
	resp.Zone = zone
	resp.Profile = z.Profile
	resp.NbMesures = n
	if n > 0 {
		resp.AvgDownload = arcepRound2(sumDl / float64(n))
		resp.AvgUpload = arcepRound2(sumUl / float64(n))
	}
	resp.SeuilDownload = prof.DownloadMbps
	resp.SeuilUpload = prof.UploadMbps
	resp.DonneesLimitees = n > 0 && n < cfg.MinSamplesForBadge

	jsonResponse(w, resp)
}

// ── Comparaison en lot pour l'Historique de l'app ────────────────────────────

// arcepBatchItem : verdict pour un test de l'historique. Chaque bloc (debit,
// navigation, streaming) n'est présent que si le composant a réellement été
// mesuré — l'app affichera une ligne par bloc présent (3 lignes test complet,
// 1 ligne test indépendant).
type arcepBatchItem struct {
	ID         string         `json:"id"`
	Found      bool           `json:"found"`
	Zone       string         `json:"zone,omitempty"`
	Profile    string         `json:"profile,omitempty"`
	SeuilDL    float64        `json:"seuilDownload,omitempty"`
	SeuilUL    float64        `json:"seuilUpload,omitempty"`
	NbMesures  int            `json:"nbMesures,omitempty"`
	Debit      *arcepQosBlock `json:"-"` // réservé : verdict débit calculé côté client
	Qos        arcepQosBlock  `json:"qos"`
	NonComparé string         `json:"nonCompare,omitempty"` // motif quand aucun bloc débit
}

// ArcepMeasureBatch répond à POST /api/arcep/measure/batch : résolution de
// zone (GPS → ville) pour une liste de tests de l'historique, en UNE requête.
// Le corps attendu :
//
//	{"tests":[{"id":"…","lat":12.33,"lng":-1.54,"city":"Ouaga, Burkina Faso"}]}
//
// Chaque item renvoie la zone + les seuils ; les comparaisons (au-dessus /
// dans le seuil) restent calculées côté client avec les valeurs du test —
// le serveur ne connaît jamais les résultats individuels.
func ArcepMeasureBatch(w http.ResponseWriter, r *http.Request) {
	arcepMu.RLock()
	cfg := arcepLoaded
	aliases := arcepAliases
	arcepMu.RUnlock()

	if cfg == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "arcep config unavailable"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !mongoRequired(w) {
		return
	}

	var body struct {
		Tests []struct {
			ID   string  `json:"id"`
			Lat  float64 `json:"lat"`
			Lng  float64 `json:"lng"`
			City string  `json:"city"`
		} `json:"tests"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.Tests) > 500 {
		body.Tests = body.Tests[:500] // garde-fou : l'historique local est borné
	}

	// Seuils du profil par zone résolus une fois pour toutes les requêtes.
	type zoneSeuils struct {
		profile string
		dl, ul  float64
	}
	seuilsParZone := make(map[string]zoneSeuils, len(cfg.Zones))
	for name, z := range cfg.Zones {
		p := cfg.Profiles[z.Profile]
		seuilsParZone[name] = zoneSeuils{profile: z.Profile, dl: p.DownloadMbps, ul: p.UploadMbps}
	}

	items := make([]arcepBatchItem, 0, len(body.Tests))
	for _, t := range body.Tests {
		item := arcepBatchItem{ID: t.ID}

		// QoS : renvoyé pour chaque item — les blocs navigation/streaming
		// s'afficheront dès lors que le composant a été mesuré, même hors zone.
		if cfg.Qos != nil {
			item.Qos.Navigation = &arcepQosNavigation{
				PageLoadSeuilS:  cfg.Qos.Navigation.PageLoadS,
				ConnectionSeuil: cfg.Qos.Navigation.ConnectionS,
				Source:          cfg.Qos.Navigation.Source,
			}
			item.Qos.Streaming = &arcepQosStreaming{
				PreloadSeuilS:       cfg.Qos.Streaming.PreloadS,
				RebufferRatioMax:    cfg.Qos.QualiteVideo.RebufferRatioMax,
				ResolutionMin:       cfg.Qos.QualiteVideo.ResolutionMin,
				InterruptionsMax:    cfg.Qos.QualiteVideo.InterruptionsMax,
				QualityPctReference: cfg.Qos.Streaming.QualityPct,
			}
		}

		// Résolution de zone : GPS d'abord (le corps porte lat/lng), repli
		// ville (même logique ipinfo que l'endpoint unitaire).
		zone := ""
		if t.Lat != 0 || t.Lng != 0 {
			zone = resolveZoneByCoords(cfg, t.Lat, t.Lng)
		}
		if zone == "" {
			candidates := append([]string{t.City}, strings.Split(t.City, ",")...)
			for _, cand := range candidates {
				cand = strings.TrimSpace(cand)
				if cand == "" {
					continue
				}
				if z, ok := aliases[normalizeCity(cand)]; ok {
					zone = z
					break
				}
			}
		}

		if zone == "" {
			item.NonComparé = "hors zone"
			items = append(items, item)
			continue
		}

		s := seuilsParZone[zone]
		item.Found = true
		item.Zone = zone
		item.Profile = s.profile
		item.SeuilDL = s.dl
		item.SeuilUL = s.ul
		items = append(items, item)
	}

	jsonResponse(w, map[string]interface{}{
		"config": map[string]interface{}{"version": cfg.Version, "source": cfg.Source, "periode": cfg.Periode},
		"items":  items,
	})
}
