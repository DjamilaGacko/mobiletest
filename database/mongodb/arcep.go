package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// ArcepCityStat agrège les mesures de débit d'une ville.
// Sert au module « Comparaison ARCEP » : le handler joint ces moyennes aux
// seuils du profil de la zone (config/seuils.json) côté applicatif.
type ArcepCityStat struct {
	City  string  `bson:"_id"   json:"city"`
	Count int     `bson:"count" json:"count"`
	AvgDl float64 `bson:"avgDl" json:"avgDownload"`
	AvgUl float64 `bson:"avgUl" json:"avgUpload"`
}

// FetchArcepCityStats regroupe, par ville (champ `city` renseigné via l'ipinfo
// de la mesure), les moyennes des tests de débit aboutis. Mêmes garde-fous que
// les autres agrégations dashboard : collecte passive exclue (speedOnly),
// conversion tolérante aux valeurs manquantes (toDoubleSafe).
func (m *MongoDB) FetchArcepCityStats() ([]ArcepCityStat, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: speedOnly(bson.M{"city": bson.M{"$ne": ""}})}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$city"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "avgDl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$dl")}}},
			{Key: "avgUl", Value: bson.D{{Key: "$avg", Value: toDoubleSafe("$ul")}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "count", Value: -1}}}},
	}

	cursor, err := m.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var agg []ArcepCityStat
	if err := cursor.All(ctx, &agg); err != nil {
		return nil, err
	}
	for i := range agg {
		agg[i].AvgDl = round2(agg[i].AvgDl)
		agg[i].AvgUl = round2(agg[i].AvgUl)
	}
	return agg, nil
}
