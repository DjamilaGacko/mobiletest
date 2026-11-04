package results

import (
	"testing"
	"time"
)

func TestDashboardToken(t *testing.T) {
	const email, password = "agent@example.org", "mot-de-passe"
	now := time.Now()
	token := signDashboardToken(email, password, now.Add(time.Hour))

	if !validDashboardToken(token, email, password, now) {
		t.Fatal("un jeton valide doit être accepté")
	}

	refused := []struct {
		name, token, email, password string
		now                          time.Time
	}{
		{"expiré", token, email, password, now.Add(2 * time.Hour)},
		{"mot de passe changé", token, email, "autre", now},
		{"adresse changée", token, "autre@example.org", password, now},
		{"échéance repoussée", signDashboardToken(email, "autre", now.Add(time.Hour)), email, password, now},
		{"vide", "", email, password, now},
		{"mal formé", "abc", email, password, now},
		{"accès non configuré", signDashboardToken("", "", now.Add(time.Hour)), "", "", now},
	}
	for _, c := range refused {
		if validDashboardToken(c.token, c.email, c.password, c.now) {
			t.Errorf("%s : le jeton aurait dû être refusé", c.name)
		}
	}
}
