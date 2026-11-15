package platform

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"testing/fstest"
	"time"

	"fooddelivery/internal/events"
)

// App объединяет зависимости типового сервиса: журнал, базу, брокер и сервер.
type App struct {
	Name    string
	Log     *slog.Logger
	DB      *sql.DB
	Brokers []string
	Pub     *events.Publisher
	Server  *Server
}

// Start подключается к PostgreSQL (если заданы миграции), применяет миграции, при необходимости
// создаёт топики Kafka и готовит HTTP-сервер. При ошибке процесс завершается,
// а Kubernetes перезапускает контейнер.
func Start(name string, migrations fs.FS, lockKey int64, useKafka bool) *App {
	log := Logger(name)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	app := &App{Name: name, Log: log, Server: NewServer(name, log)}
	var err error
	if migrations != nil {
		db, err := OpenDB(ctx, log)
		if err != nil {
			fatal(log, "database", err)
		}
		if err := Migrate(ctx, db, migrations, lockKey); err != nil {
			fatal(log, "migrations", err)
		}
		app.DB = db
		app.Server.AddCheck("postgres", db.PingContext)
	}
	if useKafka {
		app.Brokers = events.Brokers(Env("KAFKA_BROKERS", "kafka:9092"))
		for attempt := 1; ; attempt++ {
			err = events.EnsureTopics(ctx, app.Brokers, EnvInt("KAFKA_PARTITIONS", 3), EnvInt("KAFKA_REPLICATION", 1))
			if err == nil {
				break
			}
			if attempt >= 40 {
				fatal(log, "kafka", err)
			}
			log.Info("waiting for kafka", "attempt", attempt, "err", err)
			time.Sleep(3 * time.Second)
		}
		app.Pub = events.NewPublisher(app.Brokers)
		app.Server.AddCheck("kafka", func(ctx context.Context) error {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", app.Brokers[0])
			if err == nil {
				conn.Close()
			}
			return err
		})
	}
	return app
}

// Consumer возвращает фоновую задачу чтения топиков группой group.
func (a *App) Consumer(group string, topics []string, h events.Handler) func(context.Context) error {
	return func(ctx context.Context) error {
		return events.Consume(ctx, a.Log, a.Brokers, group, topics, h)
	}
}

// Relay возвращает фоновую задачу публикации исходящего буфера.
func (a *App) Relay() func(context.Context) error { return RelayOutbox(a.DB, a.Pub, a.Log) }

// Every запускает fn с заданным периодом до отмены контекста.
func (a *App) Every(period time.Duration, name string, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				if err := fn(ctx); err != nil && ctx.Err() == nil {
					a.Log.Warn("periodic job failed", "job", name, "err", err)
				}
			}
		}
	}
}

// Run запускает сервер с фоновыми задачами и закрывает ресурсы по завершении.
func (a *App) Run(jobs ...func(context.Context) error) {
	err := a.Server.Run(context.Background(), jobs...)
	if a.Pub != nil {
		a.Pub.Close() //nolint:errcheck
	}
	if a.DB != nil {
		a.DB.Close() //nolint:errcheck
	}
	if err != nil {
		os.Exit(1)
	}
}

// Tx выполняет fn в транзакции.
func (a *App) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	return tx.Commit()
}

func fatal(log *slog.Logger, what string, err error) {
	log.Error("startup failed", "stage", what, "err", err)
	os.Exit(1)
}

// MergeFS возвращает файловую систему миграций сервиса с добавленным файлом
// name: так общие таблицы создаются в схеме каждого сервиса первыми.
func MergeFS(sub fs.FS, name, content string) fs.FS {
	out := fstest.MapFS{name: &fstest.MapFile{Data: []byte(content)}}
	files, _ := fs.Glob(sub, "*.sql")
	for _, f := range files {
		if data, err := fs.ReadFile(sub, f); err == nil {
			out[f] = &fstest.MapFile{Data: data}
		}
	}
	return out
}
