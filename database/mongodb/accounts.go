package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	log "github.com/sirupsen/logrus"
)

// ── Comptes utilisateurs (IMP-03) ─────────────────────────────────────────────
//
// Le compte est facultatif : l'application reste utilisable sans, et une
// mesure anonyme reste anonyme. Il sert à retrouver ses mesures (changement
// de téléphone, réinstallation) et à séparer les historiques de plusieurs
// personnes.

// ErrEmailTaken est renvoyée quand une adresse est déjà utilisée par un compte.
var ErrEmailTaken = errors.New("email already registered")

// User est un compte. Le mot de passe n'est conservé que sous forme
// d'empreinte bcrypt et n'est jamais renvoyé par l'API.
type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty"  json:"id"`
	Email        string             `bson:"email"          json:"email"`
	Name         string             `bson:"name"           json:"name"`
	PasswordHash string             `bson:"password_hash"  json:"-"`
	CreatedAt    time.Time          `bson:"created_at"     json:"createdAt"`
}

// session : un jeton de connexion. Seule son empreinte SHA-256 est stockée,
// si bien qu'une fuite de la base ne livre aucun jeton utilisable.
type session struct {
	TokenHash string             `bson:"token_hash"`
	UserID    primitive.ObjectID `bson:"user_id"`
	CreatedAt time.Time          `bson:"created_at"`
	ExpiresAt time.Time          `bson:"expires_at"`
}

func (m *MongoDB) ensureAccountIndexes(ctx context.Context) {
	if _, err := m.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true),
	}); err != nil {
		log.Warnf("MongoDB users index warning: %s", err)
	}
	if _, err := m.sessions.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "token_hash", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "user_id", Value: 1}}},
		// MongoDB supprime lui-même les sessions expirées.
		{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	}); err != nil {
		log.Warnf("MongoDB sessions index warning: %s", err)
	}
}

func shortCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// CreateUser enregistre un compte. L'adresse doit déjà être normalisée.
func (m *MongoDB) CreateUser(email, name, passwordHash string) (*User, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	u := &User{Email: email, Name: name, PasswordHash: passwordHash, CreatedAt: time.Now()}
	res, err := m.users.InsertOne(ctx, u)
	if mongo.IsDuplicateKeyError(err) {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, err
	}
	u.ID = res.InsertedID.(primitive.ObjectID)
	return u, nil
}

// UserByEmail renvoie le compte, ou nil s'il n'existe pas.
func (m *MongoDB) UserByEmail(email string) (*User, error) {
	return m.findUser(bson.M{"email": email})
}

// UserByID renvoie le compte, ou nil s'il n'existe pas.
func (m *MongoDB) UserByID(id primitive.ObjectID) (*User, error) {
	return m.findUser(bson.M{"_id": id})
}

func (m *MongoDB) findUser(filter bson.M) (*User, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	var u User
	err := m.users.FindOne(ctx, filter).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUser modifie le nom et, si passwordHash n'est pas vide, le mot de passe.
func (m *MongoDB) UpdateUser(id primitive.ObjectID, name, passwordHash string) error {
	ctx, cancel := shortCtx()
	defer cancel()
	set := bson.M{"name": name}
	if passwordHash != "" {
		set["password_hash"] = passwordHash
	}
	_, err := m.users.UpdateByID(ctx, id, bson.M{"$set": set})
	return err
}

// DeleteUser supprime le compte et ses sessions. Les mesures restent dans la
// base — elles alimentent la carte de couverture — mais redeviennent anonymes.
func (m *MongoDB) DeleteUser(id primitive.ObjectID) error {
	ctx, cancel := shortCtx()
	defer cancel()
	if _, err := m.collection.UpdateMany(ctx,
		bson.M{"user_id": id.Hex()}, bson.M{"$unset": bson.M{"user_id": ""}}); err != nil {
		return err
	}
	if _, err := m.sessions.DeleteMany(ctx, bson.M{"user_id": id}); err != nil {
		return err
	}
	_, err := m.users.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// CreateSession enregistre l'empreinte d'un jeton valable jusqu'à expiresAt.
func (m *MongoDB) CreateSession(tokenHash string, userID primitive.ObjectID, expiresAt time.Time) error {
	ctx, cancel := shortCtx()
	defer cancel()
	_, err := m.sessions.InsertOne(ctx, session{
		TokenHash: tokenHash, UserID: userID, CreatedAt: time.Now(), ExpiresAt: expiresAt,
	})
	return err
}

// SessionUser renvoie le compte associé au jeton, ou nil si le jeton est
// inconnu ou expiré. L'index TTL ne passe qu'une fois par minute : on ne
// compte pas sur lui pour refuser un jeton tout juste expiré.
func (m *MongoDB) SessionUser(tokenHash string) (*User, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	var s session
	err := m.sessions.FindOne(ctx, bson.M{
		"token_hash": tokenHash, "expires_at": bson.M{"$gt": time.Now()},
	}).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.UserByID(s.UserID)
}

// DeleteSession ferme une session (déconnexion).
func (m *MongoDB) DeleteSession(tokenHash string) error {
	ctx, cancel := shortCtx()
	defer cancel()
	_, err := m.sessions.DeleteOne(ctx, bson.M{"token_hash": tokenHash})
	return err
}

// DeleteOtherSessions ferme toutes les sessions du compte sauf celle-ci :
// après un changement de mot de passe, un appareil qui connaissait l'ancien
// ne doit plus rester connecté.
func (m *MongoDB) DeleteOtherSessions(userID primitive.ObjectID, keepTokenHash string) error {
	ctx, cancel := shortCtx()
	defer cancel()
	_, err := m.sessions.DeleteMany(ctx, bson.M{
		"user_id": userID, "token_hash": bson.M{"$ne": keepTokenHash},
	})
	return err
}

// AttachResult rattache une mesure qui vient d'être enregistrée à un compte.
func (m *MongoDB) AttachResult(uuid string, userID primitive.ObjectID) error {
	ctx, cancel := shortCtx()
	defer cancel()
	_, err := m.collection.UpdateOne(ctx,
		bson.M{"uuid": uuid}, bson.M{"$set": bson.M{"user_id": userID.Hex()}})
	return err
}

// CountUserResults renvoie le nombre de mesures rattachées au compte.
func (m *MongoDB) CountUserResults(userID primitive.ObjectID) (int64, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	return m.collection.CountDocuments(ctx, bson.M{"user_id": userID.Hex()})
}

// UserResults renvoie les mesures du compte, les plus récentes d'abord.
// Seul le propriétaire du compte y a accès : le filtre sur user_id est ce qui
// isole les historiques.
func (m *MongoDB) UserResults(userID primitive.ObjectID, limit int64) ([]Document, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(limit)
	cursor, err := m.collection.Find(ctx, bson.M{"user_id": userID.Hex()}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	docs := []Document{}
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}
