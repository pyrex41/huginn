package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter/tmux"
)

// scriptRunner is a tmux stand-in: canned replies per subcommand and a
// list of successive screens for capture-pane.
type scriptRunner struct {
	replies map[string]string
	screens []string
	n       int
	calls   [][]string
}

func (r *scriptRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if args[0] == "capture-pane" {
		i := r.n
		if i >= len(r.screens) {
			i = len(r.screens) - 1
		}
		r.n++
		if i < 0 {
			return nil, nil
		}
		return []byte(r.screens[i]), nil
	}
	return []byte(r.replies[args[0]]), nil
}

func shellServer(t *testing.T, r tmux.Runner) *httptest.Server {
	t.Helper()
	var shells *tmux.Adapter
	if r != nil {
		shells = tmux.NewWithRunner(r)
	}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Shells: shells})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func shellCall(t *testing.T, ts *httptest.Server, token, method, params string) (json.RawMessage, *rpcError) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d", method, resp.StatusCode)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Result, out.Error
}

func TestShellListNeedsToken(t *testing.T) {
	ts := shellServer(t, &scriptRunner{})
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"shell/list","params":{}}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("shell/list without a token must be 401, got %d", resp.StatusCode)
	}
}

func TestShellVerbsAbsentUnlessEnabled(t *testing.T) {
	ts := shellServer(t, nil)
	for _, m := range ShellMethods() {
		_, rpcErr := shellCall(t, ts, "test-token", m, `{"name":"x","keys":["Enter"]}`)
		if rpcErr == nil || rpcErr.Code != CodeMethodNotFound {
			t.Fatalf("%s without --shell: %+v", m, rpcErr)
		}
	}
	// The session verbs are untouched by the absence.
	if _, rpcErr := shellCall(t, ts, "test-token", MethodList, `{}`); rpcErr != nil {
		t.Fatalf("session/list: %+v", rpcErr)
	}
}

func TestShellListPages(t *testing.T) {
	var b strings.Builder
	for _, n := range []string{"c", "a", "b"} {
		b.WriteString(n + ":1:0:1700000000:/" + n + "\n")
	}
	ts := shellServer(t, &scriptRunner{replies: map[string]string{"list-sessions": b.String()}})
	var page struct {
		Shells     []tmux.Shell `json:"shells"`
		Total      int          `json:"total"`
		NextCursor string       `json:"nextCursor"`
	}
	res, rpcErr := shellCall(t, ts, "test-token", MethodShellList, `{"limit":2}`)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	_ = json.Unmarshal(res, &page)
	if page.Total != 3 || len(page.Shells) != 2 || page.NextCursor == "" {
		t.Fatalf("page=%+v", page)
	}
	if page.Shells[0].Name != "a" || page.Shells[1].Name != "b" {
		t.Fatalf("order=%+v", page.Shells)
	}
	if page.Shells[0].Host == "" {
		t.Fatal("rows must carry host")
	}
	res, rpcErr = shellCall(t, ts, "test-token", MethodShellList, `{"limit":2,"cursor":"`+page.NextCursor+`"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	page.NextCursor = ""
	_ = json.Unmarshal(res, &page)
	if len(page.Shells) != 1 || page.Shells[0].Name != "c" || page.NextCursor != "" || page.Total != 3 {
		t.Fatalf("last page=%+v", page)
	}
	res, _ = shellCall(t, ts, "test-token", MethodShellList, `{"host":"some-other-box"}`)
	page.Total = -1
	_ = json.Unmarshal(res, &page)
	if page.Total != 0 {
		t.Fatalf("another host's shells are not ours: %+v", page)
	}
}

func TestShellScreenGenAndStaleSend(t *testing.T) {
	r := &scriptRunner{screens: []string{"$ \n", "$ \n", "$ ls\nfoo\n"}}
	ts := shellServer(t, r)
	res, rpcErr := shellCall(t, ts, "test-token", MethodShellScreen, `{"name":"build"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	var scr tmux.Screen
	_ = json.Unmarshal(res, &scr)
	if scr.Gen != tmux.Digest("$ \n") || scr.Text != "$ \n" {
		t.Fatalf("screen=%+v", scr)
	}
	// Second capture matches: send goes through.
	res, rpcErr = shellCall(t, ts, "test-token", MethodShellSend, `{"name":"build","text":"ls","enter":true,"expect_gen":"`+scr.Gen+`"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr.Message)
	}
	var sent tmux.Sent
	_ = json.Unmarshal(res, &sent)
	if sent.GenBefore != scr.Gen || sent.GenAfter != tmux.Digest("$ ls\nfoo\n") {
		t.Fatalf("sent=%+v", sent)
	}
	// Third capture has moved: the old gen is refused with the typed code.
	sends := len(r.calls)
	_, rpcErr = shellCall(t, ts, "test-token", MethodShellKeys, `{"name":"build","keys":["C-c"],"expect_gen":"`+scr.Gen+`"}`)
	if rpcErr == nil || rpcErr.Code != CodeScreenMoved {
		t.Fatalf("stale gen: %+v", rpcErr)
	}
	data, _ := rpcErr.Data.(map[string]any)
	if data["gen_before"] != tmux.Digest("$ ls\nfoo\n") {
		t.Fatalf("error data must carry gen_before: %+v", rpcErr.Data)
	}
	for _, c := range r.calls[sends:] {
		if c[0] == "send-keys" {
			t.Fatal("refused input must not reach tmux")
		}
	}
}

func TestShellParamValidation(t *testing.T) {
	ts := shellServer(t, &scriptRunner{})
	for method, params := range map[string]string{
		MethodShellScreen: `{}`,
		MethodShellSend:   `{"name":"a:b","text":"x"}`,
		MethodShellKeys:   `{"name":"a"}`,
		MethodShellNew:    `{"name":""}`,
		MethodShellKill:   `{"name":"bad.name"}`,
	} {
		_, rpcErr := shellCall(t, ts, "test-token", method, params)
		if rpcErr == nil || rpcErr.Code != CodeInvalidParams {
			t.Fatalf("%s %s: %+v", method, params, rpcErr)
		}
	}
}
