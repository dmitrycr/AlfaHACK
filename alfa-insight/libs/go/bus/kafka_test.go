package bus

import (
	"alfa.local/shared/model"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestKafkaWireDeliveryDuplicatesAndPoison(t *testing.T) {
	p := testdb.Open(t, "news")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, e := p.Exec(ctx, Tables("news")+"CREATE TABLE news.effects(id text PRIMARY KEY)"); e != nil {
		t.Fatal(e)
	}
	cluster, e := kfake.NewCluster(kfake.NumBrokers(1), kfake.BrokerConfigs(map[string]string{"message.max.bytes": "3145728"}), kfake.SeedTopics(3, Articles, "processing.dlq.v1"))
	if e != nil {
		t.Fatal(e)
	}
	defer cluster.Close()
	brokers := strings.Join(cluster.ListenAddrs(), ",")
	producer, e := NewKafka(brokers, "")
	if e != nil {
		t.Fatal(e)
	}
	defer producer.Client.Close()
	consumer, e := NewKafka(brokers, "test-consumer", Articles)
	if e != nil {
		t.Fatal(e)
	}
	defer consumer.Client.Close()
	var attempts atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx, p, "news", func(ctx context.Context, tx pgx.Tx, m model.Envelope) error {
			attempts.Add(1)
			_, e := tx.Exec(ctx, "INSERT INTO news.effects VALUES($1)", m.EntityID)
			return e
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("consumer shutdown timed out")
		}
	})
	for _, id := range []string{"one", "two", "three"} {
		m := Envelope("test", id, "public", "trace", 1, map[string]int{"x": 1})
		b, _ := json.Marshal(m)
		for i := 0; i < 2; i++ {
			if e = producer.Send(ctx, Articles, id, b); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = producer.Send(ctx, Articles, "bad", []byte(`{"invalid"`)); e != nil {
		t.Fatal(e)
	}
	large := Envelope("test", "large", "public", "trace", 1, map[string]string{"text": strings.Repeat("x", 1200000)})
	encoded, _ := json.Marshal(large)
	if e = producer.Send(ctx, Articles, "large", encoded); e != nil {
		t.Fatal("large publication message rejected", e)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var effects, poison int
		_ = p.QueryRow(ctx, "SELECT COUNT(*) FROM news.effects").Scan(&effects)
		_ = p.QueryRow(ctx, "SELECT COUNT(*) FROM news.dead_letters").Scan(&poison)
		if effects == 4 && poison == 1 {
			if attempts.Load() != 4 {
				t.Fatal("handler ran for duplicate", attempts.Load())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("messages were not processed")
}
func TestFailedPartitionDoesNotLoseOtherFetchedRecords(t *testing.T) {
	p := testdb.Open(t, "news")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, e := p.Exec(ctx, Tables("news")+"CREATE TABLE news.effects(id text PRIMARY KEY)"); e != nil {
		t.Fatal(e)
	}
	cluster, e := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, Articles))
	if e != nil {
		t.Fatal(e)
	}
	defer cluster.Close()
	producer, e := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if e != nil {
		t.Fatal(e)
	}
	defer producer.Close()
	for partition := int32(0); partition < 3; partition++ {
		id := []string{"flaky", "healthy-one", "healthy-two"}[partition]
		m := Envelope("test", id, "public", "trace", 1, map[string]int{"x": 1})
		b, _ := json.Marshal(m)
		if e = producer.ProduceSync(ctx, &kgo.Record{Topic: Articles, Partition: partition, Value: b}).FirstErr(); e != nil {
			t.Fatal(e)
		}
	}
	consumer, e := NewKafka(strings.Join(cluster.ListenAddrs(), ","), "partition-retry", Articles)
	if e != nil {
		t.Fatal(e)
	}
	defer consumer.Client.Close()
	var failures atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx, p, "news", func(ctx context.Context, tx pgx.Tx, m model.Envelope) error {
			if m.EntityID == "flaky" && failures.Add(1) <= 3 {
				return errors.New("temporary storage fault")
			}
			_, e := tx.Exec(ctx, "INSERT INTO news.effects(id) VALUES($1)", m.EntityID)
			return e
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("consumer did not stop")
		}
	}()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if e = p.QueryRow(ctx, "SELECT COUNT(*) FROM news.effects").Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n == 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("failed partition caused record loss")
}
