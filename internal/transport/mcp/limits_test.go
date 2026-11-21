package mcp

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The gate's time limits on its receipts, with a recorder that answers only once the context it was given
// has ended, as a store does whose answer comes after the caller's time ran out. Each receipt write must be
// given a deadline: a write given none would wait forever, and fails after a minute instead.

// shortLimit is the settle limit these tests give the gate. It ends each scripted write, and also bounds each
// write the script answers at once, which a busy runner can hold up for more than a few milliseconds.
const shortLimit = time.Second

// step is how one start or finish of the scripted recorder answers.
type step int

const (
	late step = iota // waits for its context to end, then writes and confirms
	lost             // waits for its context to end, then fails
)

// scripted is a recorder that answers its next starts and finishes as scripted, and every other one at once.
// Like a store, it refuses a write whose context has already ended.
type scripted struct {
	*MemoryRecorder
	mu       sync.Mutex
	starts   []step
	finishes []step
}

func (s *scripted) next(steps *[]step) (step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(*steps) == 0 {
		return 0, false
	}
	first := (*steps)[0]
	*steps = (*steps)[1:]
	return first, true
}

// await waits as the step says, and reports whether the write goes ahead.
func await(ctx context.Context, how step, scripted bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !scripted {
		return nil
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Minute):
		return errors.New("the write was given no deadline")
	}
	if how == lost {
		return ctx.Err()
	}
	return nil
}

func (s *scripted) Start(ctx context.Context, start Start) (string, error) {
	how, ok := s.next(&s.starts)
	if err := await(ctx, how, ok); err != nil {
		return "", err
	}
	return s.MemoryRecorder.Start(ctx, start)
}

func (s *scripted) Finish(ctx context.Context, auditID string, finish Finish) error {
	how, ok := s.next(&s.finishes)
	if err := await(ctx, how, ok); err != nil {
		return err
	}
	return s.MemoryRecorder.Finish(ctx, auditID, finish)
}

// overHTTPS and overStdio make one get_account call with the scripted recorder and give the fixed error the
// caller got, "" for a released result, and the receipts.
func overHTTPS(t *testing.T, starts, finishes []step, tool string) (string, []Receipt, []string) {
	t.Helper()
	h := newHarness(t)
	h.gateway.receipts = &scripted{MemoryRecorder: h.receipts, starts: starts, finishes: finishes}
	h.gateway.limit = shortLimit
	response := h.tool(tool, map[string]any{"account_id": "acc-001"}, "crm", nil)
	answer := ""
	switch {
	case response.Code == 503:
		answer = strings.TrimSpace(response.Body.String())
	case response.Code != 200 || strings.Contains(response.Body.String(), `"error"`):
		answer = fmt.Sprintf("%d %s", response.Code, response.Body)
	}
	return answer, h.receipts.Read(), h.fx.ran()
}

func overStdio(t *testing.T, starts, finishes []step, tool string) (string, []Receipt, []string) {
	t.Helper()
	var recorder *scripted
	s := newStdio(t, []string{"crm:read"}, func(c *StdioConfig) {
		recorder = &scripted{MemoryRecorder: c.Receipts.(*MemoryRecorder), starts: starts, finishes: finishes}
		c.Receipts, c.limit = recorder, shortLimit
	})
	s.send(legacyStart, legacyReady)
	s.answer(1)
	s.send(call(2, tool, `{"account_id":"acc-001"}`))
	reply := s.answer(2)
	message, _ := path(reply, "error", "message").(string)
	if message == "" && path(reply, "result", "isError") == true {
		message = fmt.Sprint(reply)
	}
	return message, recorder.MemoryRecorder.Read(), s.fx.ran()
}

// A start the store confirms only after its time ran out runs nothing: the caller is refused as for a start
// that failed, and the receipt is finished as 503 on time of its own.
func TestAStartConfirmedLateRunsNothing(t *testing.T) {
	for name, want := range map[string]string{
		"https": `{"error":"audit_unavailable; call was not executed"}`,
		"stdio": "audit_unavailable; call was not executed",
	} {
		over := map[string]func(*testing.T, []step, []step, string) (string, []Receipt, []string){
			"https": overHTTPS, "stdio": overStdio}[name]
		answer, rows, ran := over(t, []step{late}, nil, "get_account")
		if answer != want || len(ran) != 0 || len(rows) != 1 || rows[0].Status != "error" || *rows[0].HTTPStatus != 503 ||
			*rows[0].Error != "execution_or_audit_failure" {
			t.Fatalf("%s: %q, ran %v, receipts %+v", name, answer, ran, rows)
		}
	}
}

// A result whose finish the store confirms only after the call's time ran out is not released.
func TestAFinishConfirmedLateReleasesNothing(t *testing.T) {
	for name, want := range map[string]string{
		"https": `{"error":"execution_or_audit_unavailable; no result released"}`,
		"stdio": "execution_or_audit_unavailable; no result released",
	} {
		over := map[string]func(*testing.T, []step, []step, string) (string, []Receipt, []string){
			"https": overHTTPS, "stdio": overStdio}[name]
		answer, rows, _ := over(t, nil, []step{late}, "get_account")
		if answer != want || len(rows) != 1 || rows[0].Status != "ok" {
			t.Fatalf("%s: %q, receipts %+v", name, answer, rows)
		}
	}
}

// A finish that runs out of time still leaves time to record the failure: the receipt finishes as 503.
func TestAFailureIsRecordedOnItsOwnTime(t *testing.T) {
	for name, want := range map[string]string{
		"https": `{"error":"execution_or_audit_unavailable; no result released"}`,
		"stdio": "execution_or_audit_unavailable; no result released",
	} {
		over := map[string]func(*testing.T, []step, []step, string) (string, []Receipt, []string){
			"https": overHTTPS, "stdio": overStdio}[name]
		answer, rows, _ := over(t, nil, []step{lost}, "get_account")
		if answer != want || len(rows) != 1 || rows[0].Status != "error" || *rows[0].HTTPStatus != 503 {
			t.Fatalf("%s: %q, receipts %+v", name, answer, rows)
		}
	}
}

// The SDK's own error is settled on the call's deadline too: on time it is passed on, and confirmed late it
// is not released.
func TestAnSDKErrorConfirmedLateIsNotReleased(t *testing.T) {
	for _, how := range [][]step{nil, {late}} {
		h := newHarness(t)
		h.gateway.receipts = &scripted{MemoryRecorder: h.receipts, finishes: how}
		h.gateway.limit = shortLimit
		id, err := h.receipts.Start(context.Background(), Start{Tool: "get_account"})
		if err != nil {
			t.Fatal(err)
		}
		sdkError := &strictjson.Object{Keys: []string{"code", "message"}, Values: []any{big.NewInt(-32602), "Invalid params"}}
		settled := h.gateway.settle(context.Background(), &Invocation{AuditID: id}, h.clock, nil, sdkError, false)
		rows := h.receipts.Read()
		if released := settled.err != nil; released != (how == nil) || settled.withheld == released ||
			rows[0].Status != "error" || *rows[0].HTTPStatus != 400 {
			t.Fatalf("an SDK error finished %v: %+v, receipts %+v", how, settled, rows)
		}
	}
}
