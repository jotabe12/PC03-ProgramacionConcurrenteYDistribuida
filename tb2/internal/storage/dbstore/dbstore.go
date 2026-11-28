// Package dbstore implementa storage.Store con MongoDB (persistencia) y
// Redis (caché con TTL). Es el único paquete que depende de los drivers.
package dbstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
)

type Config struct {
	MongoURI      string
	MongoDB       string
	RedisAddr     string
	RedisUser     string // usuario ACL de Redis (vacío = "default")
	RedisPassword string
	Retries       int // intentos de conexión al arrancar (1 por segundo)
}

type Store struct {
	cfg    Config
	client *mongo.Client
	db     *mongo.Database
	rdb    *redis.Client
	mem    *storage.Memory
	logf   func(format string, args ...any)
}

// Open conecta con reintentos: en Docker, MongoDB puede tardar unos segundos
// en aceptar conexiones. Si un servicio no responde, se usa el respaldo en
// memoria y /api/v1/health lo informa como "desconectado" (no en silencio).
func Open(cfg Config, logf func(format string, args ...any)) *Store {
	if cfg.Retries <= 0 {
		cfg.Retries = 30
	}
	s := &Store{cfg: cfg, mem: storage.NewMemory(), logf: logf}
	if cfg.MongoURI != "" {
		for i := 1; i <= cfg.Retries; i++ {
			client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err = client.Ping(ctx, nil)
				cancel()
				if err == nil {
					s.client = client
					s.db = client.Database(cfg.MongoDB)
					logf("MongoDB conectado (%s, base %s)", redact(cfg.MongoURI), cfg.MongoDB)
					ictx, icancel := context.WithTimeout(context.Background(), 5*time.Second)
					if _, err := s.db.Collection(storage.CollUsers).Indexes().CreateOne(ictx, mongo.IndexModel{
						Keys: bson.D{{Key: "username", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
						logf("MongoDB: no se pudo crear el índice único de users: %v", err)
					}
					icancel()
					break
				}
				_ = client.Disconnect(context.Background())
			}
			logf("MongoDB no disponible (intento %d/%d): %v", i, cfg.Retries, err)
			time.Sleep(time.Second)
		}
	}
	if cfg.RedisAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Username: cfg.RedisUser, Password: cfg.RedisPassword})
		for i := 1; i <= cfg.Retries; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := rdb.Ping(ctx).Err()
			cancel()
			if err == nil {
				s.rdb = rdb
				logf("Redis conectado (%s, usuario %s)", cfg.RedisAddr, orDefault(cfg.RedisUser, "default"))
				break
			}
			logf("Redis no disponible (intento %d/%d): %v", i, cfg.Retries, err)
			time.Sleep(time.Second)
		}
		if s.rdb == nil {
			_ = rdb.Close()
		}
	}
	return s
}

// redact oculta la contraseña de la URI de MongoDB en los logs.
func redact(uri string) string {
	at := strings.LastIndex(uri, "@")
	scheme := strings.Index(uri, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return uri
	}
	cred := uri[scheme+3 : at]
	if i := strings.IndexByte(cred, ':'); i >= 0 {
		cred = cred[:i] + ":***"
	}
	return uri[:scheme+3] + cred + uri[at:]
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Store) GetUser(ctx context.Context, username string) (storage.User, error) {
	if s.db == nil {
		return s.mem.GetUser(ctx, username)
	}
	var u storage.User
	err := s.db.Collection(storage.CollUsers).FindOne(ctx, bson.D{{Key: "username", Value: username}}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return storage.User{}, storage.ErrUserNotFound
	}
	return u, err
}

func (s *Store) UpsertUser(ctx context.Context, u storage.User) error {
	if s.db == nil {
		return s.mem.UpsertUser(ctx, u)
	}
	_, err := s.db.Collection(storage.CollUsers).ReplaceOne(ctx, bson.D{{Key: "username", Value: u.Username}}, u,
		options.Replace().SetUpsert(true))
	return err
}

func (s *Store) CacheGet(ctx context.Context, key string) ([]byte, bool) {
	if s.rdb == nil {
		return s.mem.CacheGet(ctx, key)
	}
	b, err := s.rdb.Get(ctx, key).Bytes()
	if err == nil {
		return b, true
	}
	if !errors.Is(err, redis.Nil) {
		s.logf("Redis GET %s: %v", key, err)
	}
	return nil, false
}

func (s *Store) CacheSet(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if s.rdb == nil {
		s.mem.CacheSet(ctx, key, value, ttl)
		return
	}
	if err := s.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		s.logf("Redis SET %s: %v", key, err)
	}
}

func (s *Store) Insert(ctx context.Context, collection string, doc any) error {
	if s.db == nil {
		return s.mem.Insert(ctx, collection, doc)
	}
	_, err := s.db.Collection(collection).InsertOne(ctx, doc)
	return err
}

func (s *Store) Status(ctx context.Context) storage.Status {
	st := storage.Status{Mongo: "desconectado", Redis: "desconectado", Database: s.cfg.MongoDB, Collections: map[string]int64{}}
	if s.cfg.MongoURI == "" {
		st.Mongo = "no configurado"
	}
	if s.cfg.RedisAddr == "" {
		st.Redis = "no configurado"
	}
	memSt := s.mem.Status(ctx)
	if s.db != nil {
		st.Mongo = "conectado"
		for _, c := range storage.Collections {
			n, err := s.db.Collection(c).CountDocuments(ctx, bson.D{})
			if err == nil {
				st.Collections[c] = n
			}
		}
	} else {
		st.Collections = memSt.Collections
	}
	if s.rdb != nil {
		st.Redis = "conectado"
		if n, err := s.rdb.DBSize(ctx).Result(); err == nil {
			st.CacheKeys = n
		}
	} else {
		st.CacheKeys = memSt.CacheKeys
	}
	switch {
	case s.db != nil && s.rdb != nil:
		st.Mode = "MongoDB + Redis"
	case s.db != nil || s.rdb != nil:
		st.Mode = "parcial (un servicio en memoria)"
	default:
		st.Mode = "memoria (sin MongoDB/Redis)"
	}
	return st
}

func (s *Store) LastVersion(ctx context.Context) int64 {
	if s.db == nil {
		return s.mem.LastVersion(ctx)
	}
	var doc struct {
		Version int64 `bson:"version"`
	}
	opts := options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}}).SetProjection(bson.D{{Key: "version", Value: 1}})
	if err := s.db.Collection(storage.CollTrainingJobs).FindOne(ctx, bson.D{}, opts).Decode(&doc); err != nil {
		return 0 // sin documentos o sin conexión: se empieza en v1
	}
	return doc.Version
}

func (s *Store) Close() {
	if s.client != nil {
		_ = s.client.Disconnect(context.Background())
	}
	if s.rdb != nil {
		_ = s.rdb.Close()
	}
}
