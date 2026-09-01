package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestZMQWorkerDispatchesJSONRPC(t *testing.T) {
	var authorized bool
	w := &zmqWorker{
		token: "secret",
		handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			authorized = r.Header.Get("Authorization") == "Bearer secret"
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`))
		}),
	}
	got := w.dispatch([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/list","params":{}}`))
	if !authorized {
		t.Fatal("worker did not authenticate its internal broker request")
	}
	var out struct {
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &out); err != nil || !out.Result.OK {
		t.Fatalf("dispatch = %s, err=%v", got, err)
	}
}

func TestZMQWorkerRejectsWatchStream(t *testing.T) {
	w := &zmqWorker{handler: http.NotFoundHandler()}
	got := w.dispatch([]byte(`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{}}`))
	var out struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got, &out); err != nil || out.Error.Code == 0 {
		t.Fatalf("dispatch = %s, err=%v", got, err)
	}
}
