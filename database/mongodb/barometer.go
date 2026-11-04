package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// BarometerSample est une mesure réduite à ce que le baromètre compare
// (IMP-11). Les débits sont en Mb/s, la latence en ms.
type BarometerSample struct {
	Operator    string  `bson:"operator"`
	NetworkType string  `bson:"network_type"`
	Download    float64 `bson:"download"`
	Upload      float64 `bson:"upload"`
	Ping        float64 `bson:"ping"`
}

// FetchBarometerSamples renvoie les mesures de débit abouties des `days`
// derniers jours. Les tests échoués (débit nul) sont écartés, comme partout
// ailleurs : ils diraient qu'un test a raté, pas que la connexion est lente.
func (m *MongoDB) FetchBarometerSamples(days int) ([]BarometerSample, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	since := time.Now().AddDate(0, 0, -days)
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: speedOnly(bson.M{"timestamp": bson.M{"$gte": since}})}},
		{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0},
			{Key: "operator", Value: 1},
			{Key: "network_type", Value: 1},
			{Key: "download", Value: toDoubleSafe("$dl")},
			{Key: "upload", Value: toDoubleSafe("$ul")},
			{Key: "ping", Value: toDoubleSafe("$ping")},
		}}},
	}
	cursor, err := m.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	samples := []BarometerSample{}
	if err := cursor.All(ctx, &samples); err != nil {
		return nil, err
	}
	return samples, nil
}
