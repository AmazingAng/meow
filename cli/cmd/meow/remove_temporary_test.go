package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"meow.blue/meow/cli/internal/config"
)

func TestObsoletePhoneEmulatorCannotSendOrRegister(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"device":{"id":"dev_test"},"device_token":"mdt_fixture","key":{"secret":"meow_fixture"}}`))
	}))
	defer ts.Close()
	t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MEOW_SERVER", ts.URL)
	t.Setenv("MEOW_KEY", "meow_fixture")
	code := run([]string{"dev", "register", "--server", ts.URL})
	if code != exitUsage || calls != 0 {
		t.Fatalf("obsolete emulator still active: exit=%d calls=%d", code, calls)
	}
}

func TestDoctorAcceptsGatewayRegistrationWithoutAPNsToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"key":{"id":"key"},"device":{"id":"device","has_apns_token":false},"push_registered":true}`))
	}))
	defer ts.Close()
	wizardEnv(t, "meow_good")
	if err := config.Save(config.Config{Server: ts.URL, Key: "meow_good"}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"doctor"}); code != exitOK {
		t.Fatalf("gateway required a direct APNs token: exit=%d", code)
	}
}

func TestDoctorRespectsCurrentTransportRegistration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		// A token from an old direct configuration is not a current gateway binding.
		_, _ = w.Write([]byte(`{"device":{"id":"device","has_apns_token":true},"push_registered":false}`))
	}))
	defer ts.Close()
	wizardEnv(t, "meow_good")
	if err := config.Save(config.Config{Server: ts.URL, Key: "meow_good"}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"doctor"}); code == exitOK {
		t.Fatal("stale direct token hid missing current push registration")
	}
}

func TestLoginUsesConfiguredEnvironmentServer(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/whoami" || r.Header.Get("Authorization") != "Bearer meow_fixture" {
			t.Errorf("wrong request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"key":{"id":"key","hint":"meow_…"},"device":{"id":"device"}}`))
	}))
	defer ts.Close()
	t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MEOW_SERVER", ts.URL)
	t.Setenv("MEOW_KEY", "")
	if code := run([]string{"login", "--key", "meow_fixture"}); code != exitOK || calls != 1 {
		t.Fatalf("configured environment ignored: exit=%d calls=%d", code, calls)
	}
}

func TestPersistentHookExecutableDoesNotRejectOrdinaryGoBuildPaths(t *testing.T) {
	for _, test := range []struct {
		path      string
		temporary bool
	}{
		{"/opt/go-build-tools/bin/meow", false},
		{"/Users/me/go-build/meow", false},
		{"/tmp/go-build123456/b001/exe/meow", true},
		{"/tmp/go-build123456/b001/exe/my-meow", true},
		{"/opt/go-build-tools/b001/exe/meow", false},
		{"/usr/local/bin/meow", false},
	} {
		if got := isGoRunExecutable(test.path); got != test.temporary {
			t.Errorf("%s: got temporary=%v", test.path, got)
		}
	}
}
