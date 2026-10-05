package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

type failingMCPTaskStore struct {
	*inMemoryTaskStore
	unavailable atomic.Bool
}

func (s *failingMCPTaskStore) LoadMCPMentions(ctx context.Context, id string) ([]string, error) {
	if id == "anchor" && s.unavailable.Load() {
		return nil, errors.New("MCP database unavailable")
	}
	return s.inMemoryTaskStore.LoadMCPMentions(ctx, id)
}

func TestSendMessageMCP_AuthorizationFailureNeverFallsBackFresh(t *testing.T) {
	for _, status := range []string{"completed", "paused", "failed"} {
		t.Run(status, func(t *testing.T) {
			store := &failingMCPTaskStore{inMemoryTaskStore: newInMemoryTaskStore()}
			caller := &scriptedLLM{scripted: []*llm.ChatResponse{routingJSONResponse("general", 1), finishResponse("done")}}
			var observedError string
			mgr := NewManager(routingFunctionalFactory(caller), func(e Event) {
				if e.Type == "error" {
					if data, ok := e.Data.(ErrorData); ok {
						observedError = data.Error
					}
				}
			}, runtimeTempDir(t))
			t.Cleanup(mgr.Shutdown)
			mgr.SetTaskStore(store)
			info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveTask(context.Background(), TaskRecord{ID: "anchor", SessionID: info.ID, OriginalRequest: "work", Status: status, CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveMCPMentions(context.Background(), "anchor", []string{"selected"}); err != nil {
				t.Fatal(err)
			}
			sess, ok := mgr.GetSession(info.ID)
			if !ok {
				t.Fatal("GetSession returned no session")
			}
			sess.mu.Lock()
			sess.lastCompletedTaskID = "anchor"
			sess.mu.Unlock()
			store.unavailable.Store(true)
			join := func() {
				t.Helper()
				sess.mu.RLock()
				done := sess.done
				sess.mu.RUnlock()
				if done == nil {
					t.Fatal("SendMessage did not publish done channel")
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("SendMessage worker did not finish")
				}
			}
			if err := mgr.SendMessage(context.Background(), info.ID, "continue", nil, nil, nil, "", "", false, "", false, false); err != nil {
				t.Fatal(err)
			}
			join()
			if !strings.Contains(observedError, "MCP authorization state unavailable") {
				t.Errorf("SendMessage(%s) error=%q, want authorization-state error", status, observedError)
			}
			if got := len(caller.Calls()); got != 0 {
				t.Errorf("SendMessage(%s,read failure) LLM calls=%d, want 0", status, got)
			}
			store.mu.Lock()
			tasks := len(store.tasks)
			store.mu.Unlock()
			if tasks != 1 {
				t.Errorf("SendMessage(%s,read failure) tasks=%d, want original task only", status, tasks)
			}
			store.unavailable.Store(false)
			if err := mgr.SendMessage(context.Background(), info.ID, "continue", nil, nil, nil, "", "", false, "", false, false); err != nil {
				t.Fatal(err)
			}
			join()
			if got := len(caller.Calls()); got == 0 {
				t.Errorf("SendMessage(%s,recovered) LLM calls=0, want resumed execution", status)
			}
			got, err := store.LoadMCPMentions(context.Background(), "anchor")
			if err != nil || !reflect.DeepEqual(got, []string{"selected"}) {
				t.Errorf("LoadMCPMentions(recovered %s)=(%v,%v), want selected,nil", status, got, err)
			}
			store.mu.Lock()
			tasks = len(store.tasks)
			store.mu.Unlock()
			if tasks != 1 {
				t.Errorf("SendMessage(%s,recovered) tasks=%d, want original task only", status, tasks)
			}
		})
	}
}
