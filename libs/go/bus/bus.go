// Package bus implements a PostgreSQL outbox and transactional consumer inbox.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

const Articles = "news.articles.v1"
const Ingestion = "news.ingestion.v1"
const Events = "events.lifecycle.v1"

// Keep producer, consumer and broker limits aligned with 2 MiB source responses.
const MaxMessageBytes = 3 << 20

func safeSchema(s string) string {
	switch s {
	case "news", "events", "analytics":
		return s
	default:
		panic("invalid bus schema")
	}
}
func Tables(schema string) string {
	s := safeSchema(schema)
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %[1]s.outbox (
 message_id text PRIMARY KEY, topic text NOT NULL, message_key text NOT NULL,
 payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), sent_at timestamptz,
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS outbox_pending ON %[1]s.outbox(next_attempt_at) WHERE sent_at IS NULL;
CREATE TABLE IF NOT EXISTS %[1]s.inbox(message_id text PRIMARY KEY, received_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS %[1]s.dead_letters(message_id text PRIMARY KEY, reason text NOT NULL, payload bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now());`, s)
}
func Envelope(kind, entity, scope, correlation string, version int, value any) model.Envelope {
	p, _ := json.Marshal(value)
	return model.Envelope{MessageID: platform.ID(), SchemaVersion: 1, Type: kind, EntityID: entity, Scope: scope, CorrelationID: correlation, EntityVersion: version, OccurredAt: time.Now().UTC(), Payload: p}
}
func Enqueue(ctx context.Context, tx pgx.Tx, schema, topic string, msg model.Envelope) error {
	b, e := json.Marshal(msg)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO "+safeSchema(schema)+".outbox(message_id,topic,message_key,payload) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", msg.MessageID, topic, msg.EntityID, b)
	return e
}

type Producer interface {
	Send(context.Context, string, string, []byte) error
}
type Kafka struct{ Client *kgo.Client }

func NewKafka(brokers string, group string, topics ...string) (*Kafka, error) {
	options := []kgo.Opt{kgo.SeedBrokers(strings.Split(brokers, ",")...), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.ProducerBatchMaxBytes(MaxMessageBytes), kgo.FetchMaxPartitionBytes(MaxMessageBytes), kgo.FetchMaxBytes(12 << 20), kgo.ProducerBatchCompression(kgo.NoCompression()), kgo.RecordDeliveryTimeout(8 * time.Second), kgo.ProduceRequestTimeout(5 * time.Second)}
	if group != "" {
		options = append(options, kgo.ConsumerGroup(group), kgo.ConsumeTopics(topics...), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll())
	}
	c, e := kgo.NewClient(options...)
	if e != nil {
		return nil, e
	}
	return &Kafka{c}, nil
}
func (k *Kafka) Send(ctx context.Context, topic, key string, value []byte) error {
	return k.Client.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte(key), Value: value}).FirstErr()
}
func PublishOnce(ctx context.Context, p *pgxpool.Pool, schema string, producer Producer) (bool, error) {
	s := safeSchema(schema)
	tx, e := p.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var id, topic, key string
	var payload []byte
	e = tx.QueryRow(ctx, "SELECT message_id,topic,message_key,payload FROM "+s+".outbox WHERE sent_at IS NULL AND next_attempt_at<=now() ORDER BY created_at,message_id LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&id, &topic, &key, &payload)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	e = producer.Send(sendCtx, topic, key, payload)
	cancel()
	if e != nil {
		_, updateErr := tx.Exec(ctx, "UPDATE "+s+".outbox SET attempts=attempts+1,next_attempt_at=now()+LEAST(300,power(2,LEAST(attempts,8))) * interval '1 second' WHERE message_id=$1", id)
		if updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return false, commitErr
		}
		return false, e
	}
	_, e = tx.Exec(ctx, "UPDATE "+s+".outbox SET sent_at=now(),attempts=attempts+1 WHERE message_id=$1", id)
	if e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
func RunPublisher(ctx context.Context, p *pgxpool.Pool, schema string, producer Producer) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		for i := 0; i < 100; i++ {
			ok, e := PublishOnce(ctx, p, schema, producer)
			if e != nil {
				slog.Warn("outbox delivery pending", "schema", schema, "error", e)
				break
			}
			if !ok {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type Permanent struct{ Reason string }

func (e Permanent) Error() string { return e.Reason }

type Handler func(context.Context, pgx.Tx, model.Envelope) error

func Validate(m model.Envelope) error {
	if m.SchemaVersion != 1 || m.MessageID == "" || len(m.MessageID) > 128 || m.EntityID == "" || m.CorrelationID == "" || m.EntityVersion < 1 || m.OccurredAt.IsZero() || !json.Valid(m.Payload) {
		return Permanent{"invalid_envelope"}
	}
	if m.Scope != "public" && (!strings.HasPrefix(m.Scope, "company:") || len(m.Scope) <= 8) {
		return Permanent{"invalid_scope"}
	}
	return nil
}
func Process(ctx context.Context, p *pgxpool.Pool, schema string, m model.Envelope, handler Handler) error {
	if e := Validate(m); e != nil {
		return e
	}
	s := safeSchema(schema)
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "INSERT INTO "+s+".inbox(message_id) VALUES($1) ON CONFLICT DO NOTHING", m.MessageID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if e = handler(ctx, tx, m); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func quarantine(ctx context.Context, p *pgxpool.Pool, schema string, id string, raw []byte, reason string) error {
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "INSERT INTO "+safeSchema(schema)+".dead_letters(message_id,reason,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", id, reason, raw)
	if e != nil {
		return e
	}
	if tag.RowsAffected() > 0 {
		m := Envelope("message.quarantined", id, "public", id, 1, map[string]string{"reason": reason, "original_message_id": id})
		if e = Enqueue(ctx, tx, schema, "processing.dlq.v1", m); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (k *Kafka) Run(ctx context.Context, p *pgxpool.Pool, schema string, handler Handler) {
	for ctx.Err() == nil {
		fetches := k.Client.PollRecords(ctx, 20)
		if fetches.IsClientClosed() {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if ctx.Err() == nil {
				slog.Warn("Kafka fetch retry", "topic", topic, "error", err)
			}
		})
		failed := false
		// Process each partition separately: a failed partition must not discard
		// records already fetched from other partitions.
		fetches.EachPartition(func(partition kgo.FetchTopicPartition) {
			for _, record := range partition.Records {
				if ctx.Err() != nil {
					return
				}
				var message model.Envelope
				decodeErr := json.Unmarshal(record.Value, &message)
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					if decodeErr != nil {
						err = Permanent{"invalid_json"}
					} else {
						attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
						err = Process(attemptCtx, p, schema, message, handler)
						cancel()
					}
					var permanent Permanent
					if errors.As(err, &permanent) {
						id := message.MessageID
						if id == "" {
							id = platform.Hash(string(record.Value))
						}
						err = quarantine(ctx, p, schema, id, record.Value, permanent.Reason)
						break
					}
					if err == nil {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Duration(attempt+1) * time.Second):
					}
				}
				if err == nil {
					err = k.Client.CommitRecords(ctx, record)
				}
				if err != nil {
					slog.Warn("consumer retry pending", "schema", schema, "message_id", message.MessageID, "error", err)
					k.Client.SetOffsets(map[string]map[int32]kgo.EpochOffset{
						record.Topic: {record.Partition: {Epoch: record.LeaderEpoch, Offset: record.Offset}},
					})
					failed = true
					break
				}
			}
		})
		k.Client.AllowRebalance()
		if failed {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}
