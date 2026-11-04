package results

import (
	"net/url"
	"testing"

	"github.com/librespeed/speedtest-go/database/mongodb"
)

func TestShareBelow(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		v    float64
		want int
	}{
		{0, 0}, {11, 100}, {5.5, 50},
		{5, 45}, // égalité comptée pour moitié
	}
	for _, c := range cases {
		if got := shareBelow(sorted, c.v); got != c.want {
			t.Errorf("shareBelow(%v) = %d, want %d", c.v, got, c.want)
		}
	}
	if got := shareBelow(nil, 3); got != 0 {
		t.Errorf("shareBelow(nil) = %d", got)
	}
}

func TestBarometerNetworkAndOperator(t *testing.T) {
	for in, want := range map[string]string{
		"WiFi": "wifi", "Web Connection": "wifi", "4G": "mobile", "Mobile": "mobile", "": "",
	} {
		if got := barometerNetwork(in); got != want {
			t.Errorf("barometerNetwork(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"TELECEL FASO": "Telecel Faso",
		"Telecel Faso": "Telecel Faso",
		"ONATEL (Office National des Telecommunications": "ONATEL",
		"Orange Burkina Faso":                            "Orange",
		"GVA Cote d'Ivoire SAS":                          "GVA Cote d'Ivoire SAS",
	} {
		if got := barometerOperatorName(in); got != want {
			t.Errorf("barometerOperatorName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnswerBarometer(t *testing.T) {
	var samples []mongodb.BarometerSample
	for i := 1; i <= 10; i++ {
		samples = append(samples,
			mongodb.BarometerSample{Operator: "Orange Burkina Faso", NetworkType: "4G",
				Download: float64(i * 2), Upload: float64(i), Ping: float64(100 - i*5)},
			mongodb.BarometerSample{Operator: "Moov Africa", NetworkType: "Mobile",
				Download: float64(i), Upload: float64(i) / 2, Ping: float64(120 - i*5)},
			// Le Wi-Fi ne doit jamais entrer dans une comparaison mobile.
			mongodb.BarometerSample{Operator: "Canalbox", NetworkType: "WiFi",
				Download: 500, Upload: 500, Ping: 2},
		)
	}
	// Un opérateur trop peu mesuré n'apparaît pas.
	samples = append(samples, mongodb.BarometerSample{Operator: "Telecel Faso", NetworkType: "4G",
		Download: 50, Upload: 10, Ping: 30})

	snap := buildBarometer(samples)
	resp := answerBarometer(snap, url.Values{
		"network": {"4G"}, "operator": {"ORANGE BURKINA FASO"},
		"download": {"10"}, "upload": {"5"}, "ping": {"60"},
	})

	if resp.Network != "mobile" || resp.Count != 21 {
		t.Fatalf("network=%q count=%d, want mobile/21", resp.Network, resp.Count)
	}
	if len(resp.Operators) != 2 || resp.Operators[0].Name != "Orange" {
		t.Fatalf("operators = %+v", resp.Operators)
	}
	if resp.Operators[0].MedianDownload != 11 {
		t.Errorf("Orange median = %v, want 11", resp.Operators[0].MedianDownload)
	}
	if resp.You == nil || resp.You.DownloadPct <= 0 || resp.You.DownloadPct >= 100 {
		t.Fatalf("you = %+v", resp.You)
	}
	if resp.Operator == nil || resp.Operator.Name != "Orange" || resp.Operator.DownloadPct != 45 {
		t.Errorf("operator position = %+v, want Orange 45%%", resp.Operator)
	}

	// Sans résultat à situer : uniquement les médianes.
	if r := answerBarometer(snap, url.Values{"network": {"WiFi"}}); r.You != nil || r.Count != 10 {
		t.Errorf("wifi = %+v", r)
	}
}

func TestNormalizeEmail(t *testing.T) {
	for in, ok := range map[string]bool{
		" Awa.Ouedraogo@Example.BF ": true,
		"awa@localhost":              false,
		"pas une adresse":            false,
		"Awa <awa@example.bf>":       false,
	} {
		if _, got := normalizeEmail(in); got != ok {
			t.Errorf("normalizeEmail(%q) ok = %v, want %v", in, got, ok)
		}
	}
	if e, _ := normalizeEmail(" Awa@Example.BF "); e != "awa@example.bf" {
		t.Errorf("normalized = %q", e)
	}
}
