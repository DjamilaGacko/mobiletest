package mongodb

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
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
	IPAddress   string             `bson:"ip_address"         json:"ipAddress"`
	ISPInfo     string             `bson:"isp_info"           json:"ispInfo"`
	Download    string             `bson:"dl"                 json:"download"`
	Upload      string             `bson:"ul"                 json:"upload"`
	Ping        string             `bson:"ping"               json:"ping"`
	Jitter      string             `bson:"jitter"             json:"jitter"`
	ExtraRaw    string             `bson:"extra_raw"          json:"extraRaw"`
	UserAgent   string             `bson:"user_agent"         json:"userAgent"`
	Language    string             `bson:"language"           json:"language"`
	Log         string             `bson:"log"                json:"log"`
	// Mobile-specific parsed fields
	Operator    string  `bson:"operator"     json:"operator"`
	NetworkType string  `bson:"network_type" json:"networkType"`
	DeviceModel string  `bson:"device_model" json:"deviceModel"`
	Location    string  `bson:"location"     json:"location"`
	Latitude    float64 `bson:"latitude"     json:"latitude"`
	Longitude   float64 `bson:"longitude"    json:"longitude"`
	QoERating   int     `bson:"qoe_rating"   json:"qoeRating"`
	QoeSat      string  `bson:"qoe_sat"      json:"qoeSatisfaction"`
	// From ISPInfo
	City    string `bson:"city"    json:"city"`
	Country string `bson:"country" json:"country"`
}

// mobileExtra is the JSON structure sent in the `extra` field by the Flutter app.
type mobileExtra struct {
	Operator        string  `json:"operator"`
	NetworkType     string  `json:"networkType"`
	DeviceModel     string  `json:"deviceModel"`
	Location        string  `json:"location"`
	Latitude        float64 `json:"latitude"`
	Longitude       float64 `json:"longitude"`
	QoERating       int     `json:"qoeRating"`
	QoeSatisfaction string  `json:"qoeSatisfaction"`
}

type ispInfo struct {
	City    string `json:"city"`
	Country string `json:"country"`
	Org     string `json:"org"`
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
}

type MapPoint struct {
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	Operator    string  `json:"operator"`
	NetworkType string  `json:"networkType"`
	Download    float64 `json:"download"`
	Upload      float64 `json:"upload"`
	Ping        float64 `json:"ping"`
	Location    string  `json:"location"`
	Timestamp   string  `json:"timestamp"`
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

func (m *MongoDB) FetchSummary() (*SummaryStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	total, err := m.collection.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, err
	}

	today := time.Now().Truncate(24 * time.Hour)
	testsToday, _ := m.collection.CountDocuments(ctx, bson.M{"timestamp": bson.M{"$gte": today}})

	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$dl"}}}}},
			{Key: "avgUl", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$ul"}}}}},
			{Key: "avgPing", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$ping"}}}}},
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

	filter := bson.M{}
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
		{{Key: "$match", Value: bson.M{"operator": bson.M{"$ne": ""}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$operator"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$dl"}}}}},
			{Key: "avgUl", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$ul"}}}}},
			{Key: "avgPing", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$ping"}}}}},
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
	}
	if err := cursor.All(ctx, &agg); err != nil {
		return nil, err
	}
	stats := make([]OperatorStat, len(agg))
	for i, r := range agg {
		stats[i] = OperatorStat{
			Operator:    r.ID,
			Count:       r.Count,
			AvgDownload: round2(r.AvgDl),
			AvgUpload:   round2(r.AvgUl),
			AvgPing:     round2(r.AvgPing),
		}
	}
	return stats, nil
}

func (m *MongoDB) FetchMapPoints() ([]MapPoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"latitude": bson.M{"$ne": 0}, "longitude": bson.M{"$ne": 0}}
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
			Lat:         doc.Latitude,
			Lng:         doc.Longitude,
			Operator:    doc.Operator,
			NetworkType: doc.NetworkType,
			Download:    round2(dl),
			Upload:      round2(ul),
			Ping:        round2(ping),
			Location:    doc.Location,
			Timestamp:   doc.Timestamp.Format("02/01/2006 15:04"),
		})
	}
	return points, nil
}

func (m *MongoDB) FetchTimeline(days int) ([]TimelinePoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	from := time.Now().AddDate(0, 0, -days)
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"timestamp": bson.M{"$gte": from}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: "%Y-%m-%d"},
				{Key: "date", Value: "$timestamp"},
			}}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: bson.D{{Key: "$toDouble", Value: "$dl"}}}}},
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
			doc.DeviceModel = extra.DeviceModel
			doc.Location = extra.Location
			doc.Latitude = extra.Latitude
			doc.Longitude = extra.Longitude
			doc.QoERating = extra.QoERating
			doc.QoeSat = extra.QoeSatisfaction
		}
	}

	// Parse ISPInfo for city/country and fallback operator
	if data.ISPInfo != "" && data.ISPInfo != "{}" {
		var isp ispInfo
		if err := json.Unmarshal([]byte(data.ISPInfo), &isp); err == nil {
			doc.City = isp.City
			doc.Country = isp.Country
			if doc.Operator == "" && isp.Org != "" {
				doc.Operator = isp.Org
			}
		}
	}

	return doc
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
