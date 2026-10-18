//go:build linux && docker_e2e

package docker_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const deviceID = "550e8400-e29b-41d4-a716-446655440000"
const passphrase = "disposable-docker-passphrase"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "mount" {
		if len(os.Args) > 1 && os.Args[len(os.Args)-1] == passphrase {
			os.Exit(0)
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

type telegram struct {
	mu      sync.Mutex
	buttons []string
	updates []map[string]any
	nextID  int
}

func (s *telegram) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		var body struct {
			Reply struct {
				Keyboard [][]struct {
					Data string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil || len(body.Reply.Keyboard) != 1 || len(body.Reply.Keyboard[0]) != 2 {
			w.WriteHeader(400)
			return
		}
		for _, button := range body.Reply.Keyboard[0] {
			if len(button.Data) < 1 || len(button.Data) > 64 {
				w.WriteHeader(400)
				return
			}
		}
		s.buttons = []string{body.Reply.Keyboard[0][0].Data, body.Reply.Keyboard[0][1].Data}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		updates := s.updates
		if updates == nil {
			updates = []map[string]any{}
		}
		s.updates = nil
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": updates})
	default:
		w.WriteHeader(404)
	}
}

func (s *telegram) callback(t *testing.T, action int, user, chat int) {
	t.Helper()
	wait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.buttons) == 2 })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.updates = append(s.updates, map[string]any{"update_id": s.nextID, "callback_query": map[string]any{
		"id": "test", "from": map[string]int{"id": user}, "message": map[string]any{"chat": map[string]int{"id": chat}}, "data": s.buttons[action],
	}})
	if action == 1 {
		s.buttons = nil
	}
}

func wait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition did not become ready within 10 seconds")
}

func command(t *testing.T, input string, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/azath", args...)
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE=/data/caddy/pki/authorities/local/root.crt")
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if bytes.Contains(output, []byte(passphrase)) || bytes.Contains(output, []byte("docker-bot-token")) {
		t.Fatal("command output exposed disposable credentials")
	}
	return err
}

func process(t *testing.T, uid uint32, env []string, executable string, args ...string) func() {
	t.Helper()
	log, err := os.CreateTemp("", "azath-process-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = log, log
	if uid != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("process did not stop within 5 seconds")
			}
			_ = log.Close()
			data, _ := os.ReadFile(log.Name())
			if t.Failed() {
				t.Logf("%s log:\n%s", executable, data)
			}
			if bytes.Contains(data, []byte(passphrase)) || bytes.Contains(data, []byte("docker-bot-token")) {
				t.Error("process log exposed credentials")
			}
			_ = os.Remove(log.Name())
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestDockerUnlock(t *testing.T) {
	state := &telegram{}
	api := httptest.NewServer(http.HandlerFunc(state.handle))
	defer api.Close()
	dir := t.TempDir()
	// The service identity must traverse the test-owned parent directories.
	if err := os.Chmod(filepath.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "server")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(data, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	write := func(path string, content []byte) {
		t.Helper()
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	write(filepath.Join(data, "master.key"), key)
	config := fmt.Sprintf(`server:
  name: docker
  listen: 127.0.0.1:7800
  metrics_listen: 127.0.0.1:9090
  seal_token_ref: op://test/azath/seal-token
master_key:
  source: file
  path: %s/master.key
registry:
  path: %s/registry.json
devices:
  - name: synology
    uuid: %s
    disabled: false
notifications:
  telegram:
    bot_token_ref: op://test/bot/token
    chat_id: "111"
    approval_ttl: 30s
gate:
  type: telegram
  telegram:
    authorized_user_id: "42"
    approval_cache_ttl: 30s
`, data, data, deviceID)
	configPath := filepath.Join(data, "server.yaml")
	write(configPath, []byte(config))
	start := func() func() {
		stop := process(t, 1000, []string{"AZATH_SEAL_TOKEN=docker-seal-token", "AZATH_TOKEN=docker-bot-token"}, "/usr/local/bin/azath", "serve", "--dev", "--config", configPath, "--telegram-api-base", api.URL)
		wait(t, func() bool {
			resp, err := (&http.Client{Timeout: time.Second}).Get("http://127.0.0.1:9090/readyz")
			if err != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode == 200
		})
		return stop
	}
	stop := start()
	caddyfile := filepath.Join(dir, "Caddyfile")
	write(caddyfile, []byte("{\n admin off\n}\nhttps://localhost:8443 {\n tls internal\n reverse_proxy h2c://127.0.0.1:7800\n}\n"))
	process(t, 0, nil, "/usr/bin/caddy", "run", "--config", caddyfile, "--adapter", "caddyfile")
	wait(t, func() bool { _, err := os.Stat("/data/caddy/pki/authorities/local/root.crt"); return err == nil })
	client := filepath.Join(dir, "client.yaml")
	write(client, []byte(fmt.Sprintf("device:\n  name: synology\n  uuid: %s\nendpoints:\n  - https://localhost:8443\n", deviceID)))
	token := filepath.Join(dir, "token")
	write(token, []byte("docker-seal-token"))
	blob := filepath.Join(dir, "homes.sealed")
	wait(t, func() bool {
		return command(t, passphrase, "seal", "--config", client, "--seal-token-file", token, "--out", blob) == nil
	})
	mount := func() error {
		return command(t, "", "client", "--config", client, "--sealed-blob", blob, "--timeout", "1s", "--", "/test/mount")
	}
	denied := func(label string) {
		t.Helper()
		if mount() == nil {
			t.Fatalf("%s unexpectedly unlocked", label)
		}
	}
	denied("pending")
	state.callback(t, 0, 999, 111)
	time.Sleep(1500 * time.Millisecond)
	denied("wrong user")
	state.callback(t, 0, 42, 999)
	time.Sleep(1500 * time.Millisecond)
	denied("wrong chat")
	state.callback(t, 1, 42, 111)
	time.Sleep(1500 * time.Millisecond)
	denied("denial")
	state.callback(t, 0, 42, 111)
	wait(t, func() bool { return mount() == nil })
	t.Log("PASS: TLS seal, pending, wrong user/chat, deny, approve, cached unlock")
	original, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	write(blob, []byte("corrupt"))
	denied("corrupt blob")
	write(blob, original)
	stop()
	state.mu.Lock()
	state.buttons = nil
	state.mu.Unlock()
	stop = start()
	denied("restart cleared approval")
	state.callback(t, 0, 42, 111)
	wait(t, func() bool { return mount() == nil })
	t.Log("PASS: corrupt blob rejection and restart with persistent key and registry")
	stop()
	write(configPath, []byte(strings.Replace(config, "disabled: false", "disabled: true", 1)))
	state.mu.Lock()
	state.buttons = nil
	state.mu.Unlock()
	start()
	denied("disabled device")
	time.Sleep(1500 * time.Millisecond)
	state.mu.Lock()
	count := len(state.buttons)
	state.mu.Unlock()
	if count != 0 {
		t.Fatal("disabled device triggered Telegram")
	}
	t.Log("PASS: disabled device denied without Telegram prompt")
}
