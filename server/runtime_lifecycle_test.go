package server

import (
	"context"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/sidequestion"
	"io"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type runtimeSlowReader struct {
	io.ReadCloser
	read chan struct{}
	once sync.Once
}

func (s *runtimeSlowReader) Read(b []byte) (int, error) {
	s.once.Do(func() { close(s.read) })
	return s.ReadCloser.Read(b)
}

func TestRuntimeSlowBodyDoesNotPinArchive(t *testing.T) {
	m, s := runtimeFixture(t, t.TempDir())
	defer m.Close()
	defer s.Shutdown(context.Background())
	conv, e := m.pg.CreateConversation("chat", "slow", nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"message", "rename", "profile", "side"} {
		t.Run(kind, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			slow := &runtimeSlowReader{ReadCloser: reader, read: make(chan struct{})}
			r := httptest.NewRequest("POST", "/", slow)
			r.SetPathValue("id", itoa(conv.ID))
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				switch kind {
				case "message":
					s.pgSendConversationMessage(w, r)
				case "rename":
					s.pgRenameConversation(w, r)
				case "profile":
					s.pgUpdateConversation(w, r)
				case "side":
					s.handleSideQuestions(w, r, sidequestion.Parent{ConversationID: conv.ID})
				}
			}()
			select {
			case <-slow.read:
			case <-time.After(time.Second):
				t.Fatal("decode never entered")
			}
			if !s.archiveConversationMu.TryLock() {
				reader.Close()
				<-done
				t.Fatal("slow body holds archive admission guard")
			}
			s.archiveConversationMu.Unlock()
			reader.Close()
			<-done
		})
	}
}

func TestRuntimeShutdownJoinsOwnersAndRejectsWrites(t *testing.T) {
	base := t.TempDir()
	m, s := runtimeFixture(t, base)
	admitted := make(chan struct{})
	finish := make(chan struct{})
	s.goOwned(func() { close(admitted); <-s.ctx.Done(); <-finish; _ = m.pg.SetSetting("last-owned-write", "joined") })
	<-admitted
	result := make(chan error, 1)
	go func() { result <- s.Shutdown(context.Background()) }()
	select {
	case <-result:
		t.Fatal("shutdown returned before writer joined")
	case <-time.After(50 * time.Millisecond):
	}
	close(finish)
	select {
	case e := <-result:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not join")
	}
	if v, _, _ := m.pg.GetSetting("last-owned-write"); v != "joined" {
		t.Fatal("last write lost")
	}
	if s.goOwned(func() { t.Error("late owner admitted") }) {
		t.Fatal("late owner admitted")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/workspace/write", nil))
	if w.Code != 503 {
		t.Fatalf("shutdown admission %d", w.Code)
	}
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	before := runtimeTree(t, base)
	time.Sleep(300 * time.Millisecond)
	after := runtimeTree(t, base)
	if before != after {
		t.Fatal("post-close filesystem mutation")
	}
	d, e := db.OpenSQLite(filepath.Join(base, "state", "main.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if v, _, _ := d.GetSetting("last-owned-write"); v != "joined" {
		t.Fatal("write not persistent")
	}
}

func TestRuntimeShutdownClosesSlowRequestBody(t *testing.T) {
	m, s := runtimeFixture(t, t.TempDir())
	defer m.Close()
	token, e := signJWT(s.jwtKey)
	if e != nil {
		t.Fatal(e)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	slow := &runtimeSlowReader{ReadCloser: reader, read: make(chan struct{})}
	r := httptest.NewRequest("POST", "/api/workspace/write", slow)
	r.Header.Set("Authorization", "Bearer "+token)
	requestDone := make(chan struct{})
	go func() { defer close(requestDone); s.Handler().ServeHTTP(httptest.NewRecorder(), r) }()
	<-slow.read
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		reader.Close()
		<-done
		t.Fatal("Shutdown failed to close/join slow body")
	}
	select {
	case <-requestDone:
	default:
		t.Fatal("HTTP handler not joined")
	}
}

func runtimeTree(t *testing.T, base string) string {
	t.Helper()
	var out string
	filepath.WalkDir(base, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			t.Fatal(e)
		}
		if !d.IsDir() {
			info, _ := d.Info()
			out += path + info.ModTime().String() + fmt.Sprint(info.Size())
		}
		return nil
	})
	return out
}
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
