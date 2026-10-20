package web

import (
	"net/http"

	"yele-mobiletest/results"
	"yele-mobiletest/systemd"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes configure les routes HTTP du microservice.
func RegisterRoutes(r *gin.Engine, conf *systemd.Config) *gin.Engine {
	// ── Dashboard ──────────────────────────────────────────────────────────
	r.GET(conf.BaseURL+"/api/dashboard/timeline", results.DashboardTimeline)
	r.GET(conf.BaseURL+"/api/dashboard/heatmap", results.DashboardHeatmap)
	r.GET(conf.BaseURL+"/api/dashboard/stats/advanced", results.DashboardAdvancedStats)
	r.GET(conf.BaseURL+"/api/dashboard/barometer", results.BarometerHandler)

	// ── ARCEP ─────────────────────────────────────────────────────────────
	r.GET(conf.BaseURL+"/api/arcep/comparison", results.ArcepComparison)
	r.GET(conf.BaseURL+"/api/arcep/measure", results.ArcepMeasure)
	r.POST(conf.BaseURL+"/api/arcep/measure/batch", results.ArcepMeasureBatch)

	// ── IA ────────────────────────────────────────────────────────────────
	// Proxy vers le micro-service IA (prédiction + détection d'anomalies).
	// /api/ai/anomalies -> <ai_service_url>/ai/anomalies
	return r
}
