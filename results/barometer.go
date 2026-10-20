package results

import (
	"net/http"
	"strconv"

	"github.com/librespeed/speedtest-go/database"
	log "github.com/sirupsen/logrus"
)

// BarometerHandler situe un résultat dans la base des mesures existantes
// (IMP-09 — baromètre de comparaison, façon nPerf ; ancien libellé V4 : IMP-11).
//
// GET /api/dashboard/barometer?download=12.4&upload=2.1&ping=45
//
// Réponse :
//   - download/uploadPercentile : pourcentage de mesures dont le débit est
//     strictement inférieur au résultat testé ;
//   - pingPercentile : pourcentage de mesures dont la latence est pire
//     (supérieure) au résultat testé ;
//   - sampleSize : nombre de mesures actives abouties prises en compte ;
//   - operators : classement des opérateurs par débit moyen descendant.
func BarometerHandler(w http.ResponseWriter, r *http.Request) {
	if !mongoRequired(w) {
		return
	}
	download, _ := strconv.ParseFloat(r.URL.Query().Get("download"), 64)
	upload, _ := strconv.ParseFloat(r.URL.Query().Get("upload"), 64)
	ping, _ := strconv.ParseFloat(r.URL.Query().Get("ping"), 64)

	res, err := database.MongoDB.FetchBarometer(download, upload, ping)
	if err != nil {
		log.Errorf("BarometerHandler error: %s", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, res)
}
