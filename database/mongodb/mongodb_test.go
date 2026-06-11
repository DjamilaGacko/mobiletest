package mongodb

import (
	"testing"

	"github.com/librespeed/speedtest-go/database/schema"
)

func TestToDocumentParsesStreamingAndBrowsingExtra(t *testing.T) {
	m := &MongoDB{}
	data := &schema.TelemetryData{
		UUID:     "test-uuid",
		Download: "25.50",
		Upload:   "8.20",
		Ping:     "45.00",
		Jitter:   "3.10",
		Extra: `{
			"operator": "Orange Burkina Faso",
			"networkType": "4G",
			"deviceModel": "SM-S901B",
			"location": "Ouagadougou",
			"latitude": 12.3656,
			"longitude": -1.5197,
			"qoeRating": 4,
			"qoeSatisfaction": "Satisfait",
			"streamingStartupMs": 2300,
			"streamingRebufferCount": 1,
			"streamingRebufferRatio": 0.05,
			"streamingMaxResolution": "720p",
			"streamingScore": 72.5,
			"browsingAvgLoadMs": 3400,
			"browsingSuccessRate": 0.83,
			"browsingPagesTested": 6,
			"browsingScore": 68
		}`,
	}

	doc := m.toDocument(data)

	if doc.StreamingStartupMs != 2300 {
		t.Errorf("StreamingStartupMs = %d, attendu 2300", doc.StreamingStartupMs)
	}
	if doc.StreamingRebufferCount != 1 {
		t.Errorf("StreamingRebufferCount = %d, attendu 1", doc.StreamingRebufferCount)
	}
	if doc.StreamingRebufferRatio != 0.05 {
		t.Errorf("StreamingRebufferRatio = %f, attendu 0.05", doc.StreamingRebufferRatio)
	}
	if doc.StreamingMaxResolution != "720p" {
		t.Errorf("StreamingMaxResolution = %q, attendu 720p", doc.StreamingMaxResolution)
	}
	if doc.StreamingScore != 72.5 {
		t.Errorf("StreamingScore = %f, attendu 72.5", doc.StreamingScore)
	}
	if doc.BrowsingAvgLoadMs != 3400 {
		t.Errorf("BrowsingAvgLoadMs = %f, attendu 3400", doc.BrowsingAvgLoadMs)
	}
	if doc.BrowsingSuccessRate != 0.83 {
		t.Errorf("BrowsingSuccessRate = %f, attendu 0.83", doc.BrowsingSuccessRate)
	}
	if doc.BrowsingPagesTested != 6 {
		t.Errorf("BrowsingPagesTested = %d, attendu 6", doc.BrowsingPagesTested)
	}
	if doc.BrowsingScore != 68 {
		t.Errorf("BrowsingScore = %f, attendu 68", doc.BrowsingScore)
	}
	// Champs existants toujours intacts
	if doc.Operator != "Orange Burkina Faso" || doc.NetworkType != "4G" {
		t.Errorf("champs mobiles existants mal parsés : %q / %q", doc.Operator, doc.NetworkType)
	}
}

func TestToDocumentWithoutNewFields(t *testing.T) {
	// Un ancien test mobile (sans streaming/browsing) doit donner des valeurs zéro.
	m := &MongoDB{}
	data := &schema.TelemetryData{
		UUID:  "old-uuid",
		Extra: `{"operator":"Moov Africa","networkType":"3G"}`,
	}

	doc := m.toDocument(data)

	if doc.StreamingScore != 0 || doc.BrowsingScore != 0 || doc.StreamingMaxResolution != "" {
		t.Errorf("les anciens tests doivent avoir des métriques streaming/browsing à zéro, obtenu score=%f resolution=%q",
			doc.StreamingScore, doc.StreamingMaxResolution)
	}
	if doc.Operator != "Moov Africa" {
		t.Errorf("Operator = %q, attendu Moov Africa", doc.Operator)
	}
}
