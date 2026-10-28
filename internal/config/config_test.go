package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

func TestReadOnlyFrom(t *testing.T) {
	cases := map[string]bool{
		"": true, "1": true, "true": true, "yes": true, "on": true, "anything": true, "00": true,
		"0": false, "false": false, "FALSE": false, "no": false, "off": false, "OFF": false, " off ": false,
	}
	for v, want := range cases {
		if got := ReadOnlyFrom(v); got != want {
			t.Errorf("ReadOnlyFrom(%q) = %v, want %v", v, got, want)
		}
	}
	t.Setenv(EnvReadOnly, "")
	os.Unsetenv(EnvReadOnly)
	if !ReadOnly() {
		t.Error("unset must be read-only")
	}
}

func TestLockWait(t *testing.T) {
	t.Setenv(EnvLockWait, "")
	if LockWait() != DefaultLockWait {
		t.Error("default")
	}
	t.Setenv(EnvLockWait, "2.5")
	if LockWait() != 2500*time.Millisecond {
		t.Error("fractional seconds")
	}
	t.Setenv(EnvLockWait, "junk")
	if LockWait() != DefaultLockWait {
		t.Error("junk falls back to default")
	}
}

func TestDefaultHome(t *testing.T) {
	t.Setenv(EnvHome, "relative/dir")
	h, err := DefaultHome()
	if err != nil || !filepath.IsAbs(h) {
		t.Fatalf("TGAC_HOME should be made absolute: %q %v", h, err)
	}
	t.Setenv(EnvHome, "")
	h, _ = DefaultHome()
	if filepath.Base(h) != "telegram-agent-cli" || filepath.Base(filepath.Dir(h)) != ".config" {
		t.Fatalf("default home %q", h)
	}
}

func TestConfigRoundTripAndModes(t *testing.T) {
	p := Paths{Home: filepath.Join(t.TempDir(), "home")}
	if _, err := p.LoadConfig(); output.As(err).Code != output.CodeAuth {
		t.Fatalf("missing config should be an auth error: %v", err)
	}
	c := Config{APIID: 1, APIHash: "0123456789abcdef0123456789abcdef", Phone: "+15550000000"}
	if err := p.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if Mode(p.Home) != "0700" || Mode(p.Config()) != "0600" {
		t.Fatalf("modes home=%s config=%s", Mode(p.Home), Mode(p.Config()))
	}
	got, err := p.LoadConfig()
	if err != nil || got != c || got.Validate() != nil {
		t.Fatalf("round trip %+v %v", got, err)
	}
	d := got.WithDefaults("x 1.0")
	if d.DeviceModel != DefaultDeviceModel || d.AppVersion != "x 1.0" || d.LangCode != "en" ||
		d.WatcherSessionEnv != DefaultWatcherSessionEnv || d.GlobalPerHour != 30 || d.SystemVersion == "" {
		t.Fatalf("defaults %+v", d)
	}
	if (Config{APIID: 1, APIHash: "short"}).Validate() == nil {
		t.Error("short hash accepted")
	}

	// FixModes tightens loose files.
	loose := filepath.Join(p.Home, "session.json")
	if err := os.WriteFile(loose, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(p.Home, 0o755)
	p.FixModes()
	if Mode(loose) != "0600" || Mode(p.Home) != "0700" {
		t.Fatalf("FixModes: %s %s", Mode(loose), Mode(p.Home))
	}
}

func TestLock(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	a, b := p.NewLock(), p.NewLock()
	ctx := context.Background()
	if err := a.Acquire(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := b.Acquire(ctx, 400*time.Millisecond)
	if e := output.As(err); e.Code != output.CodeLockBusy || e.Exit != output.ExitLockBusy {
		t.Fatalf("second lock: %v", err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("did not wait")
	}
	a.Release()
	if err := b.Acquire(ctx, time.Second); err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.Release()
	b.Release() // idempotent
	if Mode(p.Lock()) != "0600" {
		t.Fatalf("lock file mode %s", Mode(p.Lock()))
	}
}
