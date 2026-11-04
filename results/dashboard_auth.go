package results

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/librespeed/speedtest-go/config"
)

// ── Accès au tableau de bord ──────────────────────────────────────────────────
//
// Le tableau de bord est réservé à l'adresse e-mail et au mot de passe définis
// dans SPEEDTEST_DASHBOARD_EMAIL et SPEEDTEST_DASHBOARD_PASSWORD. Ils sont
// séparés des comptes de l'application : l'inscription y étant libre et sans
// vérification de l'adresse, n'importe qui pourrait créer un compte à cette
// adresse et obtenir l'accès.
//
// Le jeton est signé, sans état côté serveur : l'instance Render s'endort après
// quinze minutes, et des sessions gardées en mémoire disparaîtraient à chaque
// réveil. Changer l'adresse ou le mot de passe invalide tous les jetons émis.
// Si l'une des deux variables est vide, personne ne peut se connecter.

const (
	dashboardSessionDuration = 7 * 24 * time.Hour
	// Limite commune à toutes les adresses IP : l'en-tête X-Forwarded-For étant
	// falsifiable, la limite par IP ne suffit pas à empêcher de deviner le mot
	// de passe.
	maxDashboardLogins = 30
)

func dashboardKey(email, password string) []byte {
	sum := sha256.Sum256([]byte("yele-dashboard\x00" + email + "\x00" + password))
	return sum[:]
}

func dashboardSignature(expires, email, password string) []byte {
	mac := hmac.New(sha256.New, dashboardKey(email, password))
	mac.Write([]byte(expires))
	return mac.Sum(nil)
}

func signDashboardToken(email, password string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return exp + "." + base64.RawURLEncoding.EncodeToString(dashboardSignature(exp, email, password))
}

func validDashboardToken(token, email, password string, now time.Time) bool {
	if email == "" || password == "" {
		return false
	}
	exp, signature, found := strings.Cut(token, ".")
	if !found {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(raw, dashboardSignature(exp, email, password)) {
		return false
	}
	expires, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && now.Unix() < expires
}

// sameSecret compare en temps constant, quelle que soit la longueur.
func sameSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func dashboardCredentials() (email, password string) {
	conf := config.LoadedConfig()
	return strings.ToLower(strings.TrimSpace(conf.DashboardEmail)), conf.DashboardPassword
}

// DashboardLogin ouvre l'accès au tableau de bord.
// POST /api/dashboard/login {email, password}
func DashboardLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	if tooManyAttempts(r) || recordAttempt("dashboard-login") > maxDashboardLogins {
		jsonError(w, http.StatusTooManyRequests, "Trop de tentatives, réessayez dans quelques minutes.")
		return
	}
	email, password := dashboardCredentials()
	if email == "" || password == "" {
		jsonError(w, http.StatusNotImplemented, "L'accès au tableau de bord n'est pas configuré sur le serveur.")
		return
	}
	given := strings.ToLower(strings.TrimSpace(c.Email))
	// Les deux comparaisons sont toujours effectuées : la durée de la réponse ne
	// révèle pas laquelle a échoué.
	emailOK, passwordOK := sameSecret(given, email), sameSecret(c.Password, password)
	if !emailOK || !passwordOK {
		jsonError(w, http.StatusUnauthorized, "Adresse ou mot de passe incorrect.")
		return
	}
	jsonResponse(w, map[string]string{
		"token": signDashboardToken(email, password, time.Now().Add(dashboardSessionDuration)),
	})
}

// RequireDashboard réserve une route aux porteurs d'un jeton du tableau de bord.
func RequireDashboard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, password := dashboardCredentials()
		if !validDashboardToken(bearerToken(r), email, password, time.Now()) {
			jsonError(w, http.StatusUnauthorized, "Connexion requise.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
