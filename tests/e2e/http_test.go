package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientCLIAndNativeBuildParity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users" || r.URL.Query().Get("active") != "true" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("X-Source", "fixture-a")
		w.Header().Add("X-Source", "fixture-b")
		_ = json.NewEncoder(w).Encode(map[string]any{"users": []string{"Ada", "Grace"}})
	}))
	defer server.Close()

	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[external]\nnetwork = true\n")
	endpoint, _ := json.Marshal(server.URL + "/users")
	source := `import "std/http" as http
call http.request with {"url": ` + string(endpoint) + `, "query": {"active": "true"}, "max_bytes": 4096} called response
show status of response
show body of response
`
	script := writeScript(t, dir, "main.sos", source)
	runnerOut, runnerErr, runnerCode := runCLI(t, dir, "run", script)
	if runnerCode != 0 || !strings.Contains(runnerOut, "Ada") || !strings.HasPrefix(runnerOut, "200\n") {
		t.Fatalf("runner exit=%d stdout=%q stderr=%q", runnerCode, runnerOut, runnerErr)
	}
	artifact := filepath.Join(dir, "http-app")
	_, buildErr, buildCode := runCLI(t, dir, "build", script, "--output", artifact)
	if buildCode != 0 {
		t.Fatalf("build exit=%d stderr=%q", buildCode, buildErr)
	}
	artifactOut, artifactErr, artifactCode := runAppArtifact(t, dir, artifact)
	if artifactCode != runnerCode || artifactOut != runnerOut || artifactErr != runnerErr {
		t.Fatalf("runner=(%d,%q,%q) artifact=(%d,%q,%q)", runnerCode, runnerOut, runnerErr, artifactCode, artifactOut, artifactErr)
	}
}

func TestHTTPListenerServesOneOwnedRequestAndShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[external]\nnetwork = true\n")
	quotedAddress, _ := json.Marshal(address)
	source := `import "std/http" as http
stream http.listen with {"address": ` + string(quotedAddress) + `, "max_body_bytes": 1024, "request_deadline": 5s, "shutdown_deadline": 1s} called requests
for each request from requests:
  call http.read_json_body with request called payload
  call http.respond_json with request, 202, {"received": message of payload} called response_id
  stop reading
show "server stopped"
`
	script := writeScript(t, dir, "server.sos", source)
	command := exec.Command(sosBin, "run", script)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })

	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = http.Post("http://"+address+"/events", "application/json", strings.NewReader(`{"message":"hello"}`))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request failed: %v stderr=%s", err, stderr.String())
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != 202 || string(body) != `{"received":"hello"}` {
		t.Fatalf("status=%d body=%q error=%v", response.StatusCode, body, readErr)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("server exit: %v stderr=%s", err, stderr.String())
	}
	if stdout.String() != "server stopped\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestHTTPExampleRejectsMalformedRequestWithoutCrashing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	_, testFile, _, _ := runtime.Caller(0)
	examplePath := filepath.Join(filepath.Dir(testFile), "..", "..", "examples", "sos", "http", "main.sos")
	source, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[external]\nnetwork = true\n")
	script := writeScript(t, dir, "main.sos", strings.Replace(string(source), "port 8080", fmt.Sprintf("port %d", port), 1))
	command := exec.Command(sosBin, "run", script)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })

	client := &http.Client{Timeout: 2 * time.Second}
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Post(fmt.Sprintf("http://127.0.0.1:%d", port), "text/plain", strings.NewReader("not json"))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request failed: %v stderr=%s", err, stderr.String())
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q read=%v stderr=%s", response.StatusCode, body, readErr, stderr.String())
	}
	response, err = client.Post(fmt.Sprintf("http://127.0.0.1:%d", port), "application/json", strings.NewReader(`{"message":"still alive"}`))
	if err != nil {
		t.Fatalf("valid request after malformed request failed: %v stderr=%s", err, stderr.String())
	}
	body, readErr = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusAccepted || string(body) != `{"received":"still alive"}` {
		t.Fatalf("second status=%d body=%q read=%v stderr=%s", response.StatusCode, body, readErr, stderr.String())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("example crashed: %v stderr=%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "sos: trace") {
		t.Fatalf("normal run output leaked internal trace telemetry: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "HTTP example stopped after one request") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestHTTPHandleEachKeepsListenerAfterRequestFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[external]\nnetwork = true\n")
	source := fmt.Sprintf(`listen for HTTP requests on loopback port %d called requests:
  request deadline 3 seconds
handle each request from requests one at a time:
  read JSON body from request called payload
  respond to request with status 202 and JSON payload
`, port)
	script := writeScript(t, dir, "main.sos", source)
	command := exec.Command(sosBin, "run", script)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	client := &http.Client{Timeout: 2 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Post(endpoint, "application/json", strings.NewReader("{bad json}"))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("first request: %v stderr=%s", err, stderr.String())
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("first status=%d stderr=%s", response.StatusCode, stderr.String())
	}
	response, err = client.Post(endpoint, "application/json", strings.NewReader(`{"message":"still alive"}`))
	if err != nil {
		t.Fatalf("second request: %v stderr=%s", err, stderr.String())
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusAccepted || !strings.Contains(string(body), "still alive") {
		t.Fatalf("second status=%d body=%q read=%v stderr=%s", response.StatusCode, body, readErr, stderr.String())
	}
}

func TestCanonicalHTTPForEachContinuesAfterRequestDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[external]\nnetwork = true\n")
	source := fmt.Sprintf(`listen for HTTP requests on loopback port %d called requests:
  request deadline 250 milliseconds
make seen 0
for each request from requests:
  assign seen seen + 1
  when seen is 1:
    wait for 3 seconds
  respond to request with status 200 and text "ready"
  when seen is 2:
    stop reading
`, port)
	script := writeScript(t, dir, "main.sos", source)
	command := exec.Command(sosBin, "run", script)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	client := &http.Client{Timeout: 2 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get(endpoint + "/slow")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("slow request: %v stderr=%s", err, stderr.String())
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("slow status=%d stderr=%s", response.StatusCode, stderr.String())
	}
	response, err = client.Get(endpoint + "/ready")
	if err != nil {
		t.Fatalf("second request did not reach handler: %v stderr=%s", err, stderr.String())
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "ready" {
		t.Fatalf("second response status=%d body=%q error=%v stderr=%s", response.StatusCode, body, err, stderr.String())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("server exit: %v stderr=%s", err, stderr.String())
	}
}

func TestHTTPRateLimitRejectsExtraRequestPromptly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[external]\nnetwork = true\n")
	source := fmt.Sprintf(`listen for HTTP requests on loopback port %d called requests:
  request deadline 3 seconds
limit requests to one each second
  keeping the first
  rejecting excess requests with status 429
  called limited
for each request from limited:
  respond to request with status 202
`, port)
	script := writeScript(t, dir, "main.sos", source)
	command := exec.Command(sosBin, "run", script)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	client := &http.Client{Timeout: 2 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get(endpoint)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("first request: %v stderr=%s", err, stderr.String())
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("first status=%d stderr=%s", response.StatusCode, stderr.String())
	}
	response, err = client.Get(endpoint)
	if err != nil {
		t.Fatalf("second request: %v stderr=%s", err, stderr.String())
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status=%d stderr=%s", response.StatusCode, stderr.String())
	}
}

func TestHTTPClientRequiresNetworkCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	dir := t.TempDir()
	endpoint, _ := json.Marshal(server.URL)
	script := writeScript(t, dir, "main.sos", "import \"std/http\" as http\ncall http.get_text with "+string(endpoint)+" called response\n")
	_, stderr, code := runCLI(t, dir, "run", script)
	if code == 0 || !strings.Contains(stderr, "network capability") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}
