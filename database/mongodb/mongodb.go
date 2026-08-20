package mongodb

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/librespeed/speedtest-go/database/schema"
	log "github.com/sirupsen/logrus"
)

type MongoDB struct {
	client     *mongo.Client
	collection *mongo.Collection
}

// Document is the MongoDB document structure stored in the collection.
type Document struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"      json:"id"`
	UUID        string             `bson:"uuid"               json:"uuid"`
	Timestamp   time.Time          `bson:"timestamp"          json:"timestamp"`
	// Champs sensibles (PII) : stockés en base mais JAMAIS renvoyés par l'API
	// publique du dashboard (json:"-"). Voir audit sécurité #1.
	IPAddress   string             `bson:"ip_address"         json:"-"`
	ISPInfo     string             `bson:"isp_info"           json:"-"`
	Download    string             `bson:"dl"                 json:"download"`
	Upload      string             `bson:"ul"                 json:"upload"`
	Ping        string             `bson:"ping"               json:"ping"`
	Jitter      string             `bson:"jitter"             json:"jitter"`
	ExtraRaw    string             `bson:"extra_raw"          json:"-"`
	UserAgent   string             `bson:"user_agent"         json:"-"`
	Language    string             `bson:"language"           json:"-"`
	Log         string             `bson:"log"                json:"-"`
	// Mobile-specific parsed fields
	Operator    string  `bson:"operator"     json:"operator"`
	NetworkType string  `bson:"network_type" json:"networkType"`
	SimOperator string  `bson:"sim_operator" json:"simOperator"`
	CellularTech string `bson:"cellular_tech" json:"cellularTech"`
	DeviceModel string  `bson:"device_model" json:"deviceModel"`
	Location    string  `bson:"location"     json:"location"`
	Latitude    float64 `bson:"latitude"     json:"latitude"`
	Longitude   float64 `bson:"longitude"    json:"longitude"`
	QoERating   int     `bson:"qoe_rating"   json:"qoeRating"`
	QoeSat      string  `bson:"qoe_sat"      json:"qoeSatisfaction"`
	// Streaming test results (zero values = test not performed)
	StreamingStartupMs     int     `bson:"streaming_startup_ms"     json:"streamingStartupMs"`
	StreamingRebufferCount int     `bson:"streaming_rebuffer_count" json:"streamingRebufferCount"`
	StreamingRebufferRatio float64 `bson:"streaming_rebuffer_ratio" json:"streamingRebufferRatio"`
	StreamingMaxResolution string  `bson:"streaming_max_resolution" json:"streamingMaxResolution"`
	StreamingScore         float64 `bson:"streaming_score"          json:"streamingScore"`
	// Browsing test results (zero values = test not performed)
	BrowsingAvgLoadMs   float64 `bson:"browsing_avg_load_ms"  json:"browsingAvgLoadMs"`
	BrowsingSuccessRate float64 `bson:"browsing_success_rate" json:"browsingSuccessRate"`
	BrowsingPagesTested int     `bson:"browsing_pages_tested" json:"browsingPagesTested"`
	BrowsingScore       float64 `bson:"browsing_score"        json:"browsingScore"`
	// Mesure passive (collecte de couverture en arrière-plan).
	// MeasurementType vaut "active" pour un test lancé par l'utilisateur et
	// "passive" pour une relève automatique sans speedtest. Vide pour les
	// enregistrements antérieurs à l'introduction du champ : ils sont tous
	// des tests actifs.
	MeasurementType string `bson:"measurement_type" json:"measurementType"`
	SignalDbm       int    `bson:"signal_dbm"       json:"signalDbm"`
	SignalLevel     int    `bson:"signal_level"     json:"signalLevel"`
	// From ISPInfo
	City    string `bson:"city"    json:"city"`
	Country string `bson:"country" json:"country"`
}

// mobileExtra is the JSON structure sent in the `extra` field by the Flutter app.
type mobileExtra struct {
	Operator        string  `json:"operator"`
	NetworkType     string  `json:"networkType"`
	SimOperator     string  `json:"simOperator"`
	CellularTech    string  `json:"cellularTech"`
	DeviceModel     string  `json:"deviceModel"`
	Location        string  `json:"location"`
	Latitude        float64 `json:"latitude"`
	Longitude       float64 `json:"longitude"`
	QoERating       int     `json:"qoeRating"`
	QoeSatisfaction string  `json:"qoeSatisfaction"`
	// Streaming test (optional, sent when the test was performed)
	StreamingStartupMs     int     `json:"streamingStartupMs"`
	StreamingRebufferCount int     `json:"streamingRebufferCount"`
	StreamingRebufferRatio float64 `json:"streamingRebufferRatio"`
	StreamingMaxResolution string  `json:"streamingMaxResolution"`
	StreamingScore         float64 `json:"streamingScore"`
	// Browsing test (optional, sent when the test was performed)
	BrowsingAvgLoadMs   float64 `json:"browsingAvgLoadMs"`
	BrowsingSuccessRate float64 `json:"browsingSuccessRate"`
	BrowsingPagesTested int     `json:"browsingPagesTested"`
	BrowsingScore       float64 `json:"browsingScore"`
	// Collecte passive de couverture : "active" (test utilisateur) ou
	// "passive" (relève automatique, sans mesure de débit).
	Type        string `json:"type"`
	SignalDbm   int    `json:"signalDbm"`
	SignalLevel int    `json:"signalLevel"`
}

// ispInfo matches the real shape stored in the isp_info field:
// {"processedString":"...","rawIspInfo":{"city":"...","country":"...","org":"...","loc":"lat,lng"}}
type ispInfo struct {
	ProcessedString string `json:"processedString"`
	RawISPInfo      struct {
		City    string `json:"city"`
		Country string `json:"country"`
		Org     string `json:"org"`
		Loc     string `json:"loc"`
	} `json:"rawIspInfo"`
}

// Dashboard response types

type SummaryStats struct {
	TotalTests  int64   `json:"totalTests"`
	AvgDownload float64 `json:"avgDownload"`
	AvgUpload   float64 `json:"avgUpload"`
	AvgPing     float64 `json:"avgPing"`
	TestsToday  int64   `json:"testsToday"`
}

type OperatorStat struct {
	Operator    string  `json:"operator"`
	Count       int     `json:"count"`
	AvgDownload float64 `json:"avgDownload"`
	AvgUpload   float64 `json:"avgUpload"`
	AvgPing     float64 `json:"avgPing"`
	// Averages computed only over tests where streaming/browsing was performed
	AvgStreamingScore float64 `json:"avgStreamingScore"`
	StreamingTests    int     `json:"streamingTests"`
	AvgBrowsingLoadMs float64 `json:"avgBrowsingLoadMs"`
	BrowsingTests     int     `json:"browsingTests"`
}

type MapPoint struct {
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	Operator    string  `json:"operator"`
	NetworkType string  `json:"networkType"`
	SimOperator string  `json:"simOperator"`
	CellularTech string `json:"cellularTech"`
	Download    float64 `json:"download"`
	Upload      float64 `json:"upload"`
	Ping        float64 `json:"ping"`
	Location    string  `json:"location"`
	Timestamp   string  `json:"timestamp"`
	// Streaming & browsing metrics (zero = test not performed)
	StreamingScore         float64 `json:"streamingScore"`
	StreamingMaxResolution string  `json:"streamingMaxResolution"`
	StreamingRebufferRatio float64 `json:"streamingRebufferRatio"`
	BrowsingAvgLoadMs      float64 `json:"browsingAvgLoadMs"`
	BrowsingScore          float64 `json:"browsingScore"`
}

type HeatPoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Intensity float64 `json:"intensity"` // 0.0 (bad) to 1.0 (excellent)
	Download  float64 `json:"download"`
}

type OperatorAdvancedStats struct {
	Operator    string  `json:"operator"`
	Count       int     `json:"count"`
	AvgDownload float64 `json:"avgDownload"`
	P25Download float64 `json:"p25Download"`
	P50Download float64 `json:"p50Download"`
	P75Download float64 `json:"p75Download"`
	P90Download float64 `json:"p90Download"`
	AvgUpload   float64 `json:"avgUpload"`
	AvgPing     float64 `json:"avgPing"`
	P50Ping     float64 `json:"p50Ping"`
}

type TimelinePoint struct {
	Date        string  `json:"date"`
	Count       int     `json:"count"`
	AvgDownload float64 `json:"avgDownload"`
}

type ResultsPage struct {
	Data  []Document `json:"data"`
	Total int64      `json:"total"`
	Page  int        `json:"page"`
	Limit int        `json:"limit"`
}

func Open(uri, dbName string) *MongoDB {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		log.Fatalf("MongoDB connect error: %s", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("MongoDB ping failed: %s", err)
	}

	coll := client.Database(dbName).Collection("speedtest_results")

	// Create indexes for fast queries
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "uuid", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "timestamp", Value: -1}}},
		{Keys: bson.D{{Key: "operator", Value: 1}}},
		{Keys: bson.D{{Key: "ip_address", Value: 1}}},
		{Keys: bson.D{{Key: "network_type", Value: 1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		log.Warnf("MongoDB index creation warning: %s", err)
	}

	log.Infof("Connected to MongoDB Atlas, database: %s", dbName)
	return &MongoDB{client: client, collection: coll}
}

// ── DataAccess interface ──────────────────────────────────────────────────────

func (m *MongoDB) Insert(data *schema.TelemetryData) error {
	doc := m.toDocument(data)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := m.collection.InsertOne(ctx, doc)
	return err
}

func (m *MongoDB) FetchByUUID(uuid string) (*schema.TelemetryData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var doc Document
	if err := m.collection.FindOne(ctx, bson.M{"uuid": uuid}).Decode(&doc); err != nil {
		return nil, err
	}
	return m.toTelemetryData(&doc), nil
}

func (m *MongoDB) FetchLast100() ([]schema.TelemetryData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(100)
	cursor, err := m.collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []Document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	result := make([]schema.TelemetryData, len(docs))
	for i, d := range docs {
		d2 := d
		result[i] = *m.toTelemetryData(&d2)
	}
	return result, nil
}

// ── Dashboard methods ─────────────────────────────────────────────────────────

// excludePassive écarte les relèves de couverture passives de toute statistique
// portant sur le débit.
//
// Ces relèves n'exécutent aucun speedtest : leurs champs dl/ul/ping valent 0.
// Sans ce filtre, elles apparaîtraient sur la carte comme des zones à
// « débit très limité » et tireraient toutes les moyennes vers le bas, alors
// qu'elles ne mesurent pas le débit du tout.
//
// `$ne` retient aussi les documents dépourvus du champ : les enregistrements
// antérieurs à l'introduction du marqueur restent donc comptés, ce qui est
// correct puisqu'ils proviennent tous de tests actifs.
var excludePassive = bson.M{"$ne": "passive"}

// withoutPassive ajoute le filtre ci-dessus à un filtre existant.
func withoutPassive(filter bson.M) bson.M {
	if filter == nil {
		filter = bson.M{}
	}
	filter["measurement_type"] = excludePassive
	return filter
}

func (m *MongoDB) FetchSummary() (*SummaryStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	total, err := m.collection.CountDocuments(ctx, withoutPassive(bson.M{}))
	if err != nil {
		return nil, err
	}

	today := time.Now().Truncate(24 * time.Hour)
	testsToday, _ := m.collection.CountDocuments(ctx,
		withoutPassive(bson.M{"timestamp": bson.M{"$gte": today}}))

	pipeline := mongo.Pipeline{
		// Exclude empty/incomplete telemetry records from the averages
		{{Key: "$match", Value: withoutPassive(bson.M{"dl": bson.M{"$nin": bson.A{"", nil}}})}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$dl")}}},
			{Key: "avgUl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$ul")}}},
			{Key: "avgPing", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$ping")}}},
		}}},
	}
	cursor, err := m.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var agg []struct {
		AvgDl   float64 `bson:"avgDl"`
		AvgUl   float64 `bson:"avgUl"`
		AvgPing float64 `bson:"avgPing"`
	}
	_ = cursor.All(ctx, &agg)

	stats := &SummaryStats{TotalTests: total, TestsToday: testsToday}
	if len(agg) > 0 {
		stats.AvgDownload = round2(agg[0].AvgDl)
		stats.AvgUpload = round2(agg[0].AvgUl)
		stats.AvgPing = round2(agg[0].AvgPing)
	}
	return stats, nil
}

func (m *MongoDB) FetchResults(page, limit int, operator, networkType, from, to string) (*ResultsPage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := withoutPassive(bson.M{})
	if operator != "" {
		filter["operator"] = bson.M{"$regex": operator, "$options": "i"}
	}
	if networkType != "" {
		filter["network_type"] = networkType
	}
	timeFilter := bson.M{}
	if from != "" {
		if t, err := time.Parse("2006-01-02", from); err == nil {
			timeFilter["$gte"] = t
		}
	}
	if to != "" {
		if t, err := time.Parse("2006-01-02", to); err == nil {
			timeFilter["$lte"] = t.Add(24 * time.Hour)
		}
	}
	if len(timeFilter) > 0 {
		filter["timestamp"] = timeFilter
	}

	total, _ := m.collection.CountDocuments(ctx, filter)
	skip := int64((page - 1) * limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "timestamp", Value: -1}}).
		SetSkip(skip).
		SetLimit(int64(limit))

	cursor, err := m.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []Document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	return &ResultsPage{Data: docs, Total: total, Page: page, Limit: limit}, nil
}

func (m *MongoDB) FetchOperatorStats() ([]OperatorStat, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: withoutPassive(bson.M{"operator": bson.M{"$ne": ""}})}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$operator"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$dl")}}},
			{Key: "avgUl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$ul")}}},
			{Key: "avgPing", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$ping")}}},
			// $avg ignores nulls: only tests where streaming/browsing was performed count
			{Key: "avgStreaming", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$streaming_score", 0}}}, "$streaming_score", nil,
			}}}}}},
			{Key: "streamingTests", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$streaming_score", 0}}}, 1, 0,
			}}}}}},
			{Key: "avgBrowsing", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$browsing_avg_load_ms", 0}}}, "$browsing_avg_load_ms", nil,
			}}}}}},
			{Key: "browsingTests", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$browsing_avg_load_ms", 0}}}, 1, 0,
			}}}}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "count", Value: -1}}}},
		{{Key: "$limit", Value: 20}},
	}
	cursor, err := m.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var agg []struct {
		ID      string  `bson:"_id"`
		Count   int     `bson:"count"`
		AvgDl   float64 `bson:"avgDl"`
		AvgUl   float64 `bson:"avgUl"`
		AvgPing float64 `bson:"avgPing"`
		// Pointers: $avg returns null when no test was performed for the operator
		AvgStreaming   *float64 `bson:"avgStreaming"`
		StreamingTests int      `bson:"streamingTests"`
		AvgBrowsing    *float64 `bson:"avgBrowsing"`
		BrowsingTests  int      `bson:"browsingTests"`
	}
	if err := cursor.All(ctx, &agg); err != nil {
		return nil, err
	}
	deref := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	stats := make([]OperatorStat, len(agg))
	for i, r := range agg {
		stats[i] = OperatorStat{
			Operator:          r.ID,
			Count:             r.Count,
			AvgDownload:       round2(r.AvgDl),
			AvgUpload:         round2(r.AvgUl),
			AvgPing:           round2(r.AvgPing),
			AvgStreamingScore: round2(deref(r.AvgStreaming)),
			StreamingTests:    r.StreamingTests,
			AvgBrowsingLoadMs: round2(deref(r.AvgBrowsing)),
			BrowsingTests:     r.BrowsingTests,
		}
	}
	return stats, nil
}

func (m *MongoDB) FetchMapPoints() ([]MapPoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := withoutPassive(bson.M{
		"latitude":  bson.M{"$ne": 0},
		"longitude": bson.M{"$ne": 0},
	})
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(1000)
	cursor, err := m.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []Document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	points := make([]MapPoint, 0, len(docs))
	for _, doc := range docs {
		dl, _ := strconv.ParseFloat(doc.Download, 64)
		ul, _ := strconv.ParseFloat(doc.Upload, 64)
		ping, _ := strconv.ParseFloat(doc.Ping, 64)
		points = append(points, MapPoint{
			Lat:          doc.Latitude,
			Lng:          doc.Longitude,
			Operator:     doc.Operator,
			NetworkType:  doc.NetworkType,
			SimOperator:  doc.SimOperator,
			CellularTech: doc.CellularTech,
			Download:     round2(dl),
			Upload:      round2(ul),
			Ping:        round2(ping),
			Location:    doc.Location,
			Timestamp:   doc.Timestamp.Format("02/01/2006 15:04"),
			StreamingScore:         round2(doc.StreamingScore),
			StreamingMaxResolution: doc.StreamingMaxResolution,
			StreamingRebufferRatio: round2(doc.StreamingRebufferRatio),
			BrowsingAvgLoadMs:      round2(doc.BrowsingAvgLoadMs),
			BrowsingScore:          round2(doc.BrowsingScore),
		})
	}
	return points, nil
}

func (m *MongoDB) FetchTimeline(days int) ([]TimelinePoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	from := time.Now().AddDate(0, 0, -days)
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: withoutPassive(bson.M{
			"timestamp": bson.M{"$gte": from},
			"dl":        bson.M{"$nin": bson.A{"", nil}},
		})}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: "%Y-%m-%d"},
				{Key: "date", Value: "$timestamp"},
			}}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$dl")}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cursor, err := m.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var agg []struct {
		Date  string  `bson:"_id"`
		Count int     `bson:"count"`
		AvgDl float64 `bson:"avgDl"`
	}
	if err := cursor.All(ctx, &agg); err != nil {
		return nil, err
	}
	points := make([]TimelinePoint, len(agg))
	for i, r := range agg {
		points[i] = TimelinePoint{Date: r.Date, Count: r.Count, AvgDownload: round2(r.AvgDl)}
	}
	return points, nil
}

func (m *MongoDB) FetchHeatmap() ([]HeatPoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	filter := withoutPassive(bson.M{
		"latitude":  bson.M{"$ne": 0},
		"longitude": bson.M{"$ne": 0},
	})
	cursor, err := m.collection.Find(ctx, filter,
		options.Find().SetProjection(bson.M{"latitude": 1, "longitude": 1, "dl": 1}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []Document
	if err = cursor.All(ctx, &docs); err != nil {
		return nil, err
	}

	// Normalize intensity: 100 Mbps = 1.0
	points := make([]HeatPoint, 0, len(docs))
	for _, d := range docs {
		dl, _ := strconv.ParseFloat(d.Download, 64)
		intensity := dl / 100.0
		if intensity > 1 {
			intensity = 1
		}
		points = append(points, HeatPoint{Lat: d.Latitude, Lng: d.Longitude, Intensity: intensity, Download: dl})
	}
	return points, nil
}

func (m *MongoDB) FetchAdvancedStats() ([]OperatorAdvancedStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: withoutPassive(bson.M{"operator": bson.M{"$ne": ""}})}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$operator"},
			{Key: "downloads", Value: bson.D{{Key: "$push", Value: toDoubleSafe("$dl")}}},
			{Key: "uploads", Value: bson.D{{Key: "$push", Value: toDoubleSafe("$ul")}}},
			{Key: "pings", Value: bson.D{{Key: "$push", Value: toDoubleSafe("$ping")}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "count", Value: -1}}}},
	}

	cursor, err := m.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	type rawAgg struct {
		ID        string    `bson:"_id"`
		Downloads []float64 `bson:"downloads"`
		Uploads   []float64 `bson:"uploads"`
		Pings     []float64 `bson:"pings"`
		Count     int       `bson:"count"`
	}
	var raws []rawAgg
	if err = cursor.All(ctx, &raws); err != nil {
		return nil, err
	}

	pct := func(sorted []float64, p float64) float64 {
		if len(sorted) == 0 {
			return 0
		}
		idx := int(float64(len(sorted)-1) * p)
		return round2(sorted[idx])
	}
	avg := func(vals []float64) float64 {
		if len(vals) == 0 {
			return 0
		}
		s := 0.0
		for _, v := range vals {
			s += v
		}
		return round2(s / float64(len(vals)))
	}

	result := make([]OperatorAdvancedStats, 0, len(raws))
	for _, r := range raws {
		sort.Float64s(r.Downloads)
		sort.Float64s(r.Pings)
		result = append(result, OperatorAdvancedStats{
			Operator:    r.ID,
			Count:       r.Count,
			AvgDownload: avg(r.Downloads),
			P25Download: pct(r.Downloads, 0.25),
			P50Download: pct(r.Downloads, 0.50),
			P75Download: pct(r.Downloads, 0.75),
			P90Download: pct(r.Downloads, 0.90),
			AvgUpload:   avg(r.Uploads),
			AvgPing:     avg(r.Pings),
			P50Ping:     pct(r.Pings, 0.50),
		})
	}
	return result, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m *MongoDB) toDocument(data *schema.TelemetryData) *Document {
	ts := data.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	doc := &Document{
		UUID:      data.UUID,
		Timestamp: ts,
		IPAddress: data.IPAddress,
		ISPInfo:   data.ISPInfo,
		Download:  data.Download,
		Upload:    data.Upload,
		Ping:      data.Ping,
		Jitter:    data.Jitter,
		ExtraRaw:  data.Extra,
		UserAgent: data.UserAgent,
		Language:  data.Language,
		Log:       data.Log,
	}

	// Parse mobile extra JSON
	if data.Extra != "" {
		var extra mobileExtra
		if err := json.Unmarshal([]byte(data.Extra), &extra); err == nil {
			doc.Operator = extra.Operator
			doc.NetworkType = extra.NetworkType
			doc.SimOperator = extra.SimOperator
			doc.CellularTech = extra.CellularTech
			doc.DeviceModel = extra.DeviceModel
			doc.Location = extra.Location
			doc.Latitude = extra.Latitude
			doc.Longitude = extra.Longitude
			doc.QoERating = extra.QoERating
			doc.QoeSat = extra.QoeSatisfaction
			doc.StreamingStartupMs = extra.StreamingStartupMs
			doc.StreamingRebufferCount = extra.StreamingRebufferCount
			doc.StreamingRebufferRatio = extra.StreamingRebufferRatio
			doc.StreamingMaxResolution = extra.StreamingMaxResolution
			doc.StreamingScore = extra.StreamingScore
			doc.BrowsingAvgLoadMs = extra.BrowsingAvgLoadMs
			doc.BrowsingSuccessRate = extra.BrowsingSuccessRate
			doc.BrowsingPagesTested = extra.BrowsingPagesTested
			doc.BrowsingScore = extra.BrowsingScore
			doc.SignalDbm = extra.SignalDbm
			doc.SignalLevel = extra.SignalLevel
			// Un `extra` sans type explicite provient d'une version antérieure
			// de l'application mobile : c'était nécessairement un test actif.
			doc.MeasurementType = extra.Type
			if doc.MeasurementType == "" {
				doc.MeasurementType = "active"
			}
		}
	}

	// Parse ISPInfo for city/country/location and fallback operator + coordinates.
	// Web tests (no `extra`) rely entirely on this IP-based info.
	if data.ISPInfo != "" && data.ISPInfo != "{}" {
		var isp ispInfo
		if err := json.Unmarshal([]byte(data.ISPInfo), &isp); err == nil {
			raw := isp.RawISPInfo
			doc.City = raw.City
			doc.Country = raw.Country

			if doc.Operator == "" && raw.Org != "" {
				doc.Operator = cleanOrg(raw.Org)
			}

			// Human-readable location label when the app didn't send one
			if doc.Location == "" {
				switch {
				case raw.City != "" && raw.Country != "":
					doc.Location = raw.City + ", " + raw.Country
				case raw.City != "":
					doc.Location = raw.City
				case raw.Country != "":
					doc.Location = raw.Country
				}
			}

			// Coordinates from IP geolocation ("lat,lng") when no GPS was sent
			if doc.Latitude == 0 && doc.Longitude == 0 && raw.Loc != "" {
				if parts := strings.Split(raw.Loc, ","); len(parts) == 2 {
					lat, errLat := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
					lng, errLng := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
					if errLat == nil && errLng == nil {
						doc.Latitude = lat
						doc.Longitude = lng
					}
				}
			}
		}
	}

	return doc
}

// cleanOrg strips the leading "AS<number> " autonomous-system prefix from an
// ISP organization string, e.g. "AS327871 ANPTIC" -> "ANPTIC", matching the
// behavior of the getIP web handler.
func cleanOrg(org string) string {
	if strings.HasPrefix(org, "AS") {
		if i := strings.IndexByte(org, ' '); i > 2 {
			digits := org[2:i]
			allDigits := true
			for _, c := range digits {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return strings.TrimSpace(org[i+1:])
			}
		}
	}
	return org
}

func (m *MongoDB) toTelemetryData(doc *Document) *schema.TelemetryData {
	return &schema.TelemetryData{
		Timestamp: doc.Timestamp,
		IPAddress: doc.IPAddress,
		ISPInfo:   doc.ISPInfo,
		Extra:     doc.ExtraRaw,
		UserAgent: doc.UserAgent,
		Language:  doc.Language,
		Download:  doc.Download,
		Upload:    doc.Upload,
		Ping:      doc.Ping,
		Jitter:    doc.Jitter,
		Log:       doc.Log,
		UUID:      doc.UUID,
	}
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

// toDoubleSafe converts a string field to double in an aggregation pipeline,
// falling back to 0 for empty strings, nulls or invalid values instead of
// failing the whole aggregation (telemetry records can have empty fields).
func toDoubleSafe(field string) bson.D {
	return bson.D{{Key: "$convert", Value: bson.D{
		{Key: "input", Value: field},
		{Key: "to", Value: "double"},
		{Key: "onError", Value: 0},
		{Key: "onNull", Value: 0},
	}}}
}
