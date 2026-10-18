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
	if filepath.Base(os.Args[0]) == "synoshare" {
		if len(os.Args) == 4 {
			f, err := os.OpenFile("/tmp/azath-mounts", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(3)
			}
			_, _ = fmt.Fprintln(f, os.Args[2])
			_ = f.Close()
		}
		if len(os.Args) == 4 && os.Args[1] == "--enc_mount" && os.Args[2] != "fail" && os.Args[3] == passphrase {
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
	sends   int
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
		s.sends++
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
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/azath", args...)
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE=/data/caddy/pki/authorities/local/root.crt")
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if bytes.Contains(output, []byte(passphrase)) || bytes.Contains(output, []byte("docker-bot-token")) {
		t.Error("command output exposed disposable credentials")
		return fmt.Errorf("command output exposed disposable credentials")
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
devices:
  - name: synology
    uuid: %s
    disabled: false
notifications:
  telegram:
    bot_token_ref: op://test/bot/token
    chat_id: "111"
    approval_ttl: 30s
    rate_limit: 100ms
gate:
  type: telegram
  telegram:
    authorized_user_id: "42"
    approval_cache_ttl: 30s
`, data, deviceID)
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

	clientContent := fmt.Sprintf("device:\n  name: synology\n  uuid: %s\nendpoints:\n  - https://localhost:8443\nshares:\n  - {name: homes, sealed_blob: %q}\n  - {name: media, sealed_blob: %q}\n", deviceID, blob, blob)
	write(client, []byte(clientContent))
	mount := func() error {
		return command(t, "", "synology", "--config", client, "--timeout", "8s")
	}
	denied := func(label string) {
		t.Helper()
		if mount() == nil {
			t.Fatalf("%s unexpectedly unlocked", label)
		}
	}
	beginMount := func() <-chan error {
		result := make(chan error, 1)
		go func() { result <- mount() }()
		return result
	}
	awaitMount := func(result <-chan error, success bool) {
		t.Helper()
		select {
		case err := <-result:
			if (err == nil) != success {
				t.Fatalf("mount success = %v, want %v", err == nil, success)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("mount did not complete")
		}
	}
	// Denial uses one share so it cannot leave another waiting request behind.
	write(client, []byte(strings.Split(clientContent, "  - {name: media")[0]))
	result := beginMount()
	state.callback(t, 0, 999, 111)
	state.callback(t, 0, 42, 999)
	time.Sleep(1500 * time.Millisecond)
	select {
	case <-result:
		t.Fatal("hostile callback completed mount")
	default:
	}
	state.callback(t, 1, 42, 111)
	awaitMount(result, false)
	time.Sleep(150 * time.Millisecond)
	write(client, []byte(clientContent))
	state.mu.Lock()
	state.buttons = nil
	state.mu.Unlock()
	result = beginMount()
	state.callback(t, 0, 42, 111)
	awaitMount(result, true)
	if err := mount(); err != nil {
		t.Fatal("cached unlock failed")
	}

	state.mu.Lock()
	sends := state.sends
	state.mu.Unlock()
	if sends != 2 {
		t.Fatalf("expected one denial and one device approval, got %d prompts", sends)
	}
	if err := os.WriteFile("/tmp/azath-mounts", nil, 0600); err != nil {
		t.Fatal(err)
	}
	write(client, []byte(clientContent+fmt.Sprintf("  - {name: fail, sealed_blob: %q}\n  - {name: backup, sealed_blob: %q}\n", blob, blob)))
	if mount() == nil {
		t.Fatal("partial failure succeeded")
	}
	mounts, err := os.ReadFile("/tmp/azath-mounts")
	if err != nil || string(mounts) != "homes\nmedia\nfail\nbackup\n" {
		t.Fatalf("partial mount order: %q, %v", mounts, err)
	}
	write(client, []byte(clientContent))
	t.Log("PASS: TLS seal, blocking approval, wrong user/chat, deny, approve, cached unlock")
	original, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	write(blob, []byte("corrupt"))
	denied("corrupt blob")
	state.mu.Lock()
	corruptSends := state.sends
	state.mu.Unlock()
	if corruptSends != sends {
		t.Fatal("corrupt blob prompted")
	}
	write(blob, original)
	stop()
	state.mu.Lock()
	state.buttons = nil
	state.mu.Unlock()
	stop = start()
	result = beginMount()
	state.callback(t, 0, 42, 111)
	awaitMount(result, true)
	t.Log("PASS: corrupt blob rejection and restart with persistent key and config")
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
