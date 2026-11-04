package results

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/librespeed/speedtest-go/config"
	"github.com/librespeed/speedtest-go/database"
	"github.com/librespeed/speedtest-go/database/mongodb"
	log "github.com/sirupsen/logrus"
)

// ── Comptes utilisateurs (IMP-03) ─────────────────────────────────────────────
//
// Authentification par jeton opaque : à la connexion, le serveur tire 32
// octets au hasard, en garde l'empreinte SHA-256 et renvoie le jeton à
// l'application, qui le joint ensuite en en-tête « Authorization: Bearer ».
// Contrairement à un JWT, un jeton opaque se révoque : la déconnexion le
// supprime, il cesse aussitôt de fonctionner.

const (
	sessionDuration   = 180 * 24 * time.Hour // une connexion tient six mois
	minPasswordLength = 8
	maxNameLength     = 60
)

type accountResponse struct {
	Token string        `json:"token,omitempty"`
	User  *mongodb.User `json:"user"`
	Tests int64         `json:"tests"`
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func readJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "Requête invalide.")
		return false
	}
	return true
}

func normalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.Index(email, "@"):], ".") {
		return "", false
	}
	return email, true
}

func cleanName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > maxNameLength {
		name = string(r[:maxNameLength])
	}
	return name
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// UserFromRequest renvoie le compte du jeton joint à la requête, ou nil si la
// requête est anonyme ou le jeton invalide. N'écrit rien dans la réponse :
// la télémétrie l'utilise pour rattacher une mesure sans jamais la refuser.
func UserFromRequest(r *http.Request) *mongodb.User {
	token := bearerToken(r)
	if token == "" || database.MongoDB == nil {
		return nil
	}
	u, err := database.MongoDB.SessionUser(hashToken(token))
	if err != nil {
		log.Warnf("session lookup error: %s", err)
		return nil
	}
	return u
}

// requireUser renvoie le compte connecté, ou répond 401 et renvoie nil.
func requireUser(w http.ResponseWriter, r *http.Request) *mongodb.User {
	if !mongoRequired(w) {
		return nil
	}
	u := UserFromRequest(r)
	if u == nil {
		jsonError(w, http.StatusUnauthorized, "Session expirée, reconnectez-vous.")
	}
	return u
}

func openSession(w http.ResponseWriter, u *mongodb.User) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := database.MongoDB.CreateSession(hashToken(token), u.ID, time.Now().Add(sessionDuration)); err != nil {
		log.Errorf("create session error: %s", err)
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	tests, _ := database.MongoDB.CountUserResults(u.ID)
	jsonResponse(w, accountResponse{Token: token, User: u, Tests: tests})
}

// ── Limitation des tentatives ─────────────────────────────────────────────────
// Sans elle, un mot de passe se devine par essais successifs. Mémoire locale
// au processus : suffisant pour une seule instance Render.

const (
	attemptWindow = 15 * time.Minute
	maxAttempts   = 10
)

var (
	attemptsMu sync.Mutex
	attempts   = map[string][]time.Time{}
)

func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// tooManyAttempts enregistre une tentative et dit si la limite est dépassée.
func tooManyAttempts(r *http.Request) bool {
	return recordAttempt(clientIP(r)) > maxAttempts
}

// recordAttempt enregistre une tentative et renvoie le nombre de tentatives de
// cette clé sur la période.
func recordAttempt(key string) int {
	attemptsMu.Lock()
	defer attemptsMu.Unlock()
	now := time.Now()
	recent := attempts[key][:0]
	for _, t := range attempts[key] {
		if now.Sub(t) < attemptWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	attempts[key] = recent
	if len(attempts) > 10000 { // garde-fou mémoire
		attempts = map[string][]time.Time{key: recent}
	}
	return len(recent)
}

// ── Gestionnaires ─────────────────────────────────────────────────────────────

// Register crée un compte et ouvre une session.
// POST /api/auth/register {email, password, name}
func Register(w http.ResponseWriter, r *http.Request) {
	if !mongoRequired(w) {
		return
	}
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	if tooManyAttempts(r) {
		jsonError(w, http.StatusTooManyRequests, "Trop de tentatives, réessayez dans quelques minutes.")
		return
	}
	email, ok := normalizeEmail(c.Email)
	if !ok {
		jsonError(w, http.StatusBadRequest, "Adresse e-mail invalide.")
		return
	}
	if len([]rune(c.Password)) < minPasswordLength {
		jsonError(w, http.StatusBadRequest, "Le mot de passe doit faire au moins 8 caractères.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt refuse au-delà de 72 octets
		jsonError(w, http.StatusBadRequest, "Mot de passe trop long.")
		return
	}
	u, err := database.MongoDB.CreateUser(email, cleanName(c.Name), string(hash))
	if errors.Is(err, mongodb.ErrEmailTaken) {
		jsonError(w, http.StatusConflict, "Un compte existe déjà avec cette adresse.")
		return
	}
	if err != nil {
		log.Errorf("create user error: %s", err)
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	openSession(w, u)
}

// Login ouvre une session.
// POST /api/auth/login {email, password}
func Login(w http.ResponseWriter, r *http.Request) {
	if !mongoRequired(w) {
		return
	}
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	if tooManyAttempts(r) {
		jsonError(w, http.StatusTooManyRequests, "Trop de tentatives, réessayez dans quelques minutes.")
		return
	}
	email, _ := normalizeEmail(c.Email)
	u, err := database.MongoDB.UserByEmail(email)
	if err != nil {
		log.Errorf("login lookup error: %s", err)
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	// Même message que l'adresse soit inconnue ou le mot de passe faux : sinon
	// on révélerait quelles adresses ont un compte.
	if u == nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)) != nil {
		jsonError(w, http.StatusUnauthorized, "Adresse ou mot de passe incorrect.")
		return
	}
	openSession(w, u)
}

// Logout ferme la session du jeton joint.
// POST /api/auth/logout
func Logout(w http.ResponseWriter, r *http.Request) {
	if !mongoRequired(w) {
		return
	}
	if token := bearerToken(r); token != "" {
		if err := database.MongoDB.DeleteSession(hashToken(token)); err != nil {
			log.Warnf("logout error: %s", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Me renvoie le profil du compte connecté.
// GET /api/auth/me
func Me(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	tests, _ := database.MongoDB.CountUserResults(u.ID)
	jsonResponse(w, accountResponse{User: u, Tests: tests})
}

// UpdateMe modifie le nom et, sur présentation du mot de passe actuel, le
// mot de passe.
// PUT /api/auth/me {name, currentPassword?, newPassword?}
func UpdateMe(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var body struct {
		Name            string `json:"name"`
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	newHash := ""
	if body.NewPassword != "" {
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.CurrentPassword)) != nil {
			jsonError(w, http.StatusForbidden, "Mot de passe actuel incorrect.")
			return
		}
		if len([]rune(body.NewPassword)) < minPasswordLength {
			jsonError(w, http.StatusBadRequest, "Le mot de passe doit faire au moins 8 caractères.")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "Mot de passe trop long.")
			return
		}
		newHash = string(hash)
	}
	u.Name = cleanName(body.Name)
	if err := database.MongoDB.UpdateUser(u.ID, u.Name, newHash); err != nil {
		log.Errorf("update user error: %s", err)
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	if newHash != "" {
		database.MongoDB.DeleteOtherSessions(u.ID, hashToken(bearerToken(r)))
	}
	tests, _ := database.MongoDB.CountUserResults(u.ID)
	jsonResponse(w, accountResponse{User: u, Tests: tests})
}

// DeleteMe supprime le compte, après confirmation du mot de passe.
// DELETE /api/auth/me {password}
func DeleteMe(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Password)) != nil {
		jsonError(w, http.StatusForbidden, "Mot de passe incorrect.")
		return
	}
	if err := database.MongoDB.DeleteUser(u.ID); err != nil {
		log.Errorf("delete user error: %s", err)
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MyResults renvoie les mesures du compte connecté, dans le format que
// l'application envoie (champ `extra`), complété des valeurs mesurées : elle
// peut ainsi reconstruire son historique sur un nouveau téléphone.
// GET /api/auth/results
func MyResults(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	docs, err := database.MongoDB.UserResults(u.ID, 1000)
	if err != nil {
		log.Errorf("user results error: %s", err)
		jsonError(w, http.StatusInternalServerError, "Erreur interne.")
		return
	}
	out := make([]map[string]interface{}, 0, len(docs))
	for _, d := range docs {
		item := map[string]interface{}{}
		if d.ExtraRaw != "" {
			json.Unmarshal([]byte(d.ExtraRaw), &item)
		}
		// Même identifiant que celui renvoyé à l'envoi de la mesure : c'est
		// lui que l'application a gardé, et c'est ce qui évite les doublons.
		item["id"] = d.UUID
		if config.LoadedConfig().EnableIDObfuscation {
			item["id"] = ObfuscateULID(d.UUID)
		}
		item["timestamp"] = d.Timestamp
		item["download"] = d.Download
		item["upload"] = d.Upload
		item["ping"] = d.Ping
		item["jitter"] = d.Jitter
		item["location"] = d.Location
		item["isUploaded"] = true
		out = append(out, item)
	}
	jsonResponse(w, out)
}
