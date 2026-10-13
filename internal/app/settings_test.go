package app

import "testing"

func TestDomainFromEnv(t *testing.T) {
	for in, want := range map[string]string{"": "localhost", "test": "test", "dev.example.test": "dev.example.test"} {
		got, err := DomainFromEnv(env{"MUTTI_DOMAIN": in}.get)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"Test", "a..b", ".test", "a_b"} {
		if _, err := DomainFromEnv(env{"MUTTI_DOMAIN": bad}.get); appErrCode(err) != CodeInvalidConfig {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
