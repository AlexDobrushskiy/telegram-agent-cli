package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestSuccessEnvelope(t *testing.T) {
	var b bytes.Buffer
	if err := Success(&b, map[string]any{"text": "<a & b>"}); err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"1","ok":true,"data":{"text":"<a & b>"}}` + "\n"
	if b.String() != want {
		t.Fatalf("got %s", b.String())
	}
	b.Reset()
	_ = Success(&b, nil)
	if b.String() != `{"schema_version":"1","ok":true,"data":{}}`+"\n" {
		t.Fatalf("nil data: %s", b.String())
	}
}

func TestFailureEnvelopeAndExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
		exit int
	}{
		{Usage("x"), CodeUsage, 2},
		{Auth("x"), CodeAuth, 3},
		{ReadOnly("x"), CodeReadOnly, 3},
		{NotFound("x"), CodeNotFound, 4},
		{Ambiguous("x"), CodeAmbiguous, 4},
		{Telegram("x"), CodeTelegram, 5},
		{FloodWait(7), CodeFloodWait, 5},
		{LockBusy("x"), CodeLockBusy, 6},
		{WatcherRefused("x"), CodeWatcherRefused, 7},
		{errors.New("plain"), CodeInternal, 1},
		{fmt.Errorf("wrapped: %w", NotFound("inner")), CodeNotFound, 4},
	}
	for _, c := range cases {
		var b bytes.Buffer
		exit := Failure(&b, c.err)
		var env struct {
			SchemaVersion string `json:"schema_version"`
			OK            bool   `json:"ok"`
			Error         struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if exit != c.exit || env.OK || env.SchemaVersion != "1" || env.Error.Code != c.code || env.Error.Message == "" {
			t.Errorf("%v: exit %d env %+v", c.err, exit, env)
		}
	}
}

func TestErrorEnvelopeNeverExitsZero(t *testing.T) {
	for _, err := range []error{&Error{Code: "custom", Message: "no exit set"}, Usage("x"), errors.New("plain")} {
		var b bytes.Buffer
		if exit := Failure(&b, err); exit == 0 {
			t.Errorf("%v: error envelope exited 0", err)
		}
	}
}
