//go:build integration

package tests

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Read a bounded RESP2/RESP3 frame, including HELLO maps. This test proxy drops exactly one
// XADD response AFTER reading it from real Redis, leaving a genuinely ambiguous client outcome.
func respFrame(r *bufio.Reader, depth int) ([]byte, error) {
	if depth > 16 {
		return nil, fmt.Errorf("RESP nesting")
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > 65536 || len(line) < 3 {
		return nil, fmt.Errorf("RESP line")
	}
	kind := line[0]
	result := append([]byte(nil), line...)
	if kind == '$' || kind == '=' || kind == '!' {
		n, e := strconv.Atoi(strings.TrimSpace(string(line[1:])))
		if e != nil || n > 1<<20 {
			return nil, fmt.Errorf("RESP bulk")
		}
		if n >= 0 {
			data := make([]byte, n+2)
			if _, e = io.ReadFull(r, data); e != nil {
				return nil, e
			}
			result = append(result, data...)
		}
	} else if kind == '*' || kind == '%' || kind == '~' || kind == '>' {
		n, e := strconv.Atoi(strings.TrimSpace(string(line[1:])))
		if e != nil || n > 1024 {
			return nil, fmt.Errorf("RESP aggregate")
		}
		if kind == '%' {
			n *= 2
		}
		for range n {
			data, e := respFrame(r, depth+1)
			if e != nil {
				return nil, e
			}
			result = append(result, data...)
		}
	}
	return result, nil
}
func TestOutboxLostXADDResponse(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	s, raw := streams(t)
	create(t, repo, "uncertainty", "one", request(t, db))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dropped atomic.Bool
	var wg sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			incoming, e := listener.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			connections[incoming] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer incoming.Close()
				defer func() { mu.Lock(); delete(connections, incoming); mu.Unlock() }()
				upstream, e := net.DialTimeout("tcp", os.Getenv("TEST_REDIS_ADDR"), time.Second)
				if e != nil {
					return
				}
				defer upstream.Close()
				_ = incoming.SetDeadline(time.Now().Add(10 * time.Second))
				_ = upstream.SetDeadline(time.Now().Add(10 * time.Second))
				in := bufio.NewReader(incoming)
				out := bufio.NewReader(upstream)
				for {
					command, e := respFrame(in, 0)
					if e != nil {
						return
					}
					if _, e = upstream.Write(command); e != nil {
						return
					}
					reply, e := respFrame(out, 0)
					if e != nil {
						return
					}
					if bytes.Contains(bytes.ToUpper(command), []byte("\r\nXADD\r\n")) && dropped.CompareAndSwap(false, true) {
						return
					}
					if _, e = incoming.Write(reply); e != nil {
						return
					}
				}
			}()
		}
	}()
	defer func() {
		listener.Close()
		mu.Lock()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	}()
	client := queue.Open(listener.Addr().String(), "")
	defer client.Close()
	o := relayOptions()
	o.Batch = 1
	o.PublishTimeout = 250 * time.Millisecond
	relay := outbox.Relay{Store: repo, Publisher: client.Streams(s.Stream, s.Group), Options: o}
	if err = relay.Step(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := repo.Stats(ctx)
	if err != nil || !dropped.Load() || st.Pending != 1 || st.Published != 0 || raw.XLen(ctx, s.Stream).Val() != 1 {
		t.Fatal("L uncertain publish mishandled", st, err, dropped.Load())
	}
	eventually(t, 2*time.Second, func() bool {
		if e := relay.Step(ctx); e != nil {
			t.Fatal(e)
		}
		st, e := repo.Stats(ctx)
		return e == nil && st.Published == 1
	})
	entries, err := raw.XRange(ctx, s.Stream, "-", "+").Result()
	if err != nil || len(entries) != 2 || entries[0].Values["event_id"] != entries[1].Values["event_id"] {
		t.Fatal(entries, err)
	}
	t.Log("L: real Redis accepted XADD; TCP proxy discarded its reply; relay retained intent and safely republished the same event ID")
}

func TestOutboxPermanentRedisConfiguration(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	s, raw := streams(t)
	if err := raw.Del(ctx, s.Stream).Err(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Set(ctx, s.Stream, "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	create(t, repo, "quarantine", "one", request(t, db))
	if err := (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := repo.Stats(ctx)
	if err != nil || st.Quarantined != 1 || st.Published != 0 {
		t.Fatal(st, err)
	}
	var code string
	if err = db.QueryRow(ctx, `SELECT last_error_code FROM outbox_events`).Scan(&code); err != nil || code != "redis_configuration" {
		t.Fatal(code, err)
	}
}
