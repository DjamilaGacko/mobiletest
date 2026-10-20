package web

import (
	api "github.com/librespeed/speedtest-go/results"
	"github.com/gin-gonic/gin"
	config "github.com/librespeed/speedtest-go/config"
)

// RegisterRoutes configure les routes HTTP du microservice.
func RegisterRoutes(r *gin.Engine, conf *config.Config) *gin.Engine {
	// ── Dashboard ──────────────────────────────────────────────────────────
	r.GET(conf.BaseURL+"/api/dashboard/timeline", api.DashboardTimeline)
	r.GET(conf.BaseURL+"/api/dashboard/heatmap", api.DashboardHeatmap)
	r.GET(conf.BaseURL+"/api/dashboard/stats/advanced", api.DashboardAdvancedStats)
	r.GET(conf.BaseURL+"/api/dashboard/barometer", api.BarometerHandler)

	// ── ARCEP ─────────────────────────────────────────────────────────────
	r.GET(conf.BaseURL+"/api/arcep/comparison", api.ArcepComparison)
	r.GET(conf.BaseURL+"/api/arcep/measure", api.ArcepMeasure)
	r.POST(conf.BaseURL+"/api/arcep/measure/batch", api.ArcepMeasureBatch)

	// ── IA ────────────────────────────────────────────────────────────────
	// Proxy vers le micro-service IA (prédiction + détection d'anomalies).
	// /api/ai/anomalies -> <ai_service_url>/api/ai/anomalies
	return r
}
