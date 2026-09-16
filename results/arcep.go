// Package arcep (fichier) implémente le module « Comparaison ARCEP » de Yélé.
//
// Les seuils officiels (profils D1–D4, zones du rapport) ne vivent PAS dans le
// code : ils sont lus depuis config/seuils.json, monté en volume dans Docker.
// Modifier un seuil = éditer le JSON + redémarrer le backend + committer le
// fichier. Voir seuil.md à la racine du projet pour la conception complète.
//
// Règle d'honnêteté (cf. seuils.md métier) : la comparaison est SIMPLIFIÉE
// (moyenne vs seuil), pas la méthode ARCEP officielle (90 % des mesures au-
// dessus du seuil). L'API ne renvoie donc jamais de « conforme » — uniquement
// des faits (auDessus: true/false) que le dashboard affiche tels quels.
package results

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
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
}

type arcepConfig struct {
	Version            int                     `json:"version"`
	Source             string                  `json:"source"`
	MethodNote         string                  `json:"method_note"`
	MinSamplesForBadge int                     `json:"min_samples_for_badge"`
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

// ── Réponse de l'endpoint ────────────────────────────────────────────────────

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
	} `json:"config"`
	MethodNote string            `json:"methodNote"`
	Zones      []arcepZoneResult `json:"zones"`
	HorsZone   int               `json:"horsZone"`
}

// ── Endpoint ─────────────────────────────────────────────────────────────────

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
