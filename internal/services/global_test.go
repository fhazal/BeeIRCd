package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/state"
)

func userOn(t *testing.T, h *fakeHost, uid, nick, server string) *state.User {
	t.Helper()
	u := &state.User{UID: uid, Nick: nick, Ident: "~" + strings.ToLower(nick), Host: server, RealHost: server,
		Server: server, Modes: map[byte]bool{}}
	if err := h.Emit(&state.UserAdd{User: u}); err != nil {
		t.Fatal(err)
	}
	h.svc.Flush()
	return u
}

func TestOperServReload(t *testing.T) {
	svc, h := newServices(t)
	os := h.net.UserByNick("OperServ")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	small := helpUser(t, h, "1LFAAAAAC", "Small")
	h.opers = map[string]string{boss.UID: "oa", small.UID: "og"}

	svc.Message(small.UID, os, "RELOAD")
	if got := noticeTo(h, "Small"); !strings.Contains(got, "You need oper flag a") || len(h.rehashes) != 0 {
		t.Fatalf("RELOAD without flag a: %q, rehashes %v", got, h.rehashes)
	}
	svc.Message(boss.UID, os, "RELOAD")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Reloaded the config.") || len(h.rehashes) != 1 || h.rehashes[0] != "Boss" {
		t.Fatalf("RELOAD: %q, rehashes %v", got, h.rehashes)
	}
	h.rehashErr = errors.New("beeircd.conf not used: bad port")
	svc.Message(boss.UID, os, "RELOAD")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "beeircd.conf not used: bad port") || strings.Contains(got, "Reloaded") {
		t.Fatalf("RELOAD with a bad file: %q", got)
	}

	// RELOAD is config, not services data: READONLY lets it through.
	h.rehashErr = nil
	svc.Message(boss.UID, os, "SET READONLY ON")
	noticeTo(h, "Boss")
	svc.Message(boss.UID, os, "RELOAD")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Reloaded the config.") {
		t.Errorf("RELOAD in read-only mode: %q", got)
	}
}

func TestGlobalQueue(t *testing.T) {
	svc, h := newServices(t)
	gl := h.net.UserByNick("Global")
	op := helpUser(t, h, "1LFAAAAAB", "Op")
	plain := helpUser(t, h, "1LFAAAAAC", "Plain")
	h.opers = map[string]string{op.UID: "og"}

	svc.Message(op.UID, gl, "QUEUE LIST")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Your queue is empty.") {
		t.Errorf("empty LIST: %q", got)
	}
	svc.Message(op.UID, gl, "GLOBAL")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Syntax: GLOBAL [text]") {
		t.Errorf("GLOBAL with no text and no queue: %q", got)
	}
	svc.Message(op.UID, gl, "QUEUE ADD first line")
	svc.Message(op.UID, gl, "QUEUE ADD second line")
	svc.Message(op.UID, gl, "QUEUE ADD third line")
	noticeTo(h, "Op")
	svc.Message(op.UID, gl, "QUEUE LIST")
	if got := noticeTo(h, "Op"); got != "1. first line\n2. second line\n3. third line" {
		t.Errorf("LIST: %q", got)
	}
	svc.Message(op.UID, gl, "QUEUE DEL 2")
	svc.Message(op.UID, gl, "QUEUE DEL 9")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Deleted line 2") || !strings.Contains(got, "No line 9") {
		t.Errorf("DEL: %q", got)
	}

	// GLOBAL with no text sends the queue in order and empties it.
	svc.Message(op.UID, gl, "GLOBAL")
	if got := noticeTo(h, "Plain"); got != "first line\nthird line" {
		t.Errorf("queue sent to Plain: %q", got)
	}
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Sent to everyone online") {
		t.Errorf("reply: %q", got)
	}
	svc.Message(op.UID, gl, "QUEUE LIST")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Your queue is empty.") {
		t.Errorf("queue after sending: %q", got)
	}

	// OperServ GLOBAL shares the code: text goes out, an empty one sends the queue.
	os := h.net.UserByNick("OperServ")
	svc.Message(op.UID, gl, "QUEUE ADD from the queue")
	noticeTo(h, "Op")
	svc.Message(op.UID, os, "GLOBAL")
	if got := noticeTo(h, "Plain"); got != "from the queue" {
		t.Errorf("OS GLOBAL with the queue: %q", got)
	}
	svc.Message(op.UID, os, "GLOBAL hello all")
	if got := noticeTo(h, "Plain"); got != "hello all" {
		t.Errorf("OS GLOBAL text: %q", got)
	}
	noticeTo(h, "Op")
	svc.Message(op.UID, os, "GLOBAL")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Syntax: GLOBAL [text]") {
		t.Errorf("OS GLOBAL with nothing: %q", got)
	}

	// CLEAR, and the cap of 10 lines.
	for i := 0; i < maxGlobalQueue; i++ {
		svc.Message(op.UID, gl, "QUEUE ADD line")
	}
	noticeTo(h, "Op")
	svc.Message(op.UID, gl, "QUEUE ADD one too many")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "10 lines already") {
		t.Errorf("11th line: %q", got)
	}
	if len(op2queue(svc, op)) != maxGlobalQueue {
		t.Errorf("queue holds %d lines", len(op2queue(svc, op)))
	}
	svc.Message(op.UID, gl, "QUEUE CLEAR")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "empty now") || len(op2queue(svc, op)) != 0 {
		t.Errorf("CLEAR: %q", got)
	}

	// Queues are per person: Plain is no oper and may not use any of it.
	svc.Message(plain.UID, gl, "QUEUE ADD sneaky")
	if got := noticeTo(h, "Plain"); !strings.Contains(got, "for IRC operators") {
		t.Errorf("a plain user on Global: %q", got)
	}
}

func op2queue(svc *Services, u *state.User) []string { return svc.clients[u.UID].globalQueue }

func TestGlobalServer(t *testing.T) {
	svc, h := newServices(t)
	gl := h.net.UserByNick("Global")
	op := userOn(t, h, "1LFAAAAAB", "Op", "hub.test")
	near := userOn(t, h, "1LFAAAAAC", "Near", "leaf.test")
	near2 := userOn(t, h, "1LFAAAAAD", "Near2", "LEAF.test")
	far := userOn(t, h, "1LFAAAAAE", "Far", "far.test")
	_, _, _ = near, near2, far
	small := userOn(t, h, "1LFAAAAAF", "Small", "hub.test")
	h.opers = map[string]string{op.UID: "og", small.UID: "oa"}

	svc.Message(op.UID, gl, "SERVER leaf.test Maintenance at noon")
	for _, n := range []string{"Near", "Near2"} {
		if got := noticeTo(h, n); got != "Maintenance at noon" {
			t.Errorf("%s: %q", n, got)
		}
	}
	if got := noticeTo(h, "Far"); got != "" {
		t.Errorf("Far is on another server: %q", got)
	}
	if got := noticeTo(h, "Op"); !strings.Contains(got, "Sent to everyone on leaf.test (2 people).") {
		t.Errorf("reply: %q", got)
	}

	svc.Message(op.UID, gl, "SERVER nowhere.test hi")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "No server named nowhere.test.") {
		t.Errorf("unknown server: %q", got)
	}
	if got := noticeTo(h, "Near"); got != "" {
		t.Errorf("unknown server still sent: %q", got)
	}

	// No text: the queue goes to that server, and only there.
	svc.Message(op.UID, gl, "QUEUE ADD one")
	svc.Message(op.UID, gl, "QUEUE ADD two")
	noticeTo(h, "Op")
	svc.Message(op.UID, gl, "server FAR.test")
	if got := noticeTo(h, "Far"); got != "one\ntwo" {
		t.Errorf("queue to Far: %q", got)
	}
	if got := noticeTo(h, "Near"); got != "" {
		t.Errorf("queue leaked to Near: %q", got)
	}
	noticeTo(h, "Op")
	svc.Message(op.UID, gl, "SERVER far.test")
	if got := noticeTo(h, "Op"); !strings.Contains(got, "queue is empty") {
		t.Errorf("SERVER with nothing to send: %q", got)
	}

	// Without flag g nothing works, whatever other flags the oper has.
	svc.Message(small.UID, gl, "SERVER far.test no way")
	svc.Message(small.UID, gl, "GLOBAL no way")
	svc.Message(small.UID, gl, "QUEUE ADD no way")
	got := noticeTo(h, "Small")
	if strings.Count(got, "You need oper flag g") != 3 || noticeTo(h, "Far") != "" {
		t.Errorf("an oper without g: %q", got)
	}
}

// Global's commands aren't services data: READONLY never blocks them.
func TestGlobalNotBlockedByReadOnly(t *testing.T) {
	svc, h := newServices(t)
	gl := h.net.UserByNick("Global")
	op := helpUser(t, h, "1LFAAAAAB", "Op")
	other := helpUser(t, h, "1LFAAAAAC", "Other")
	h.opers = map[string]string{op.UID: "oag"}
	svc.readonly = true

	if svc.readonlyBlocks(svc.clients[op.UID], svc.global, "QUEUE", []string{"ADD", "x"}) {
		t.Error("readonlyBlocks stopped Global QUEUE")
	}
	svc.Message(op.UID, gl, "GLOBAL still works")
	if got := noticeTo(h, "Other"); got != "still works" {
		t.Errorf("Global in read-only mode: %q", got)
	}
	_ = other
}
