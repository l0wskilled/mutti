package xdg

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Dirs
	}{
		{
			name: "fallbacks under HOME",
			env:  map[string]string{"HOME": "/home/u"},
			want: Dirs{State: "/home/u/.local/state/mutti", Data: "/home/u/.local/share/mutti", Config: "/home/u/.config/mutti"},
		},
		{
			name: "XDG vars win",
			env:  map[string]string{"HOME": "/home/u", "XDG_STATE_HOME": "/s", "XDG_DATA_HOME": "/d", "XDG_CONFIG_HOME": "/c"},
			want: Dirs{State: "/s/mutti", Data: "/d/mutti", Config: "/c/mutti"},
		},
		{
			name: "relative XDG vars are ignored as the spec demands",
			env:  map[string]string{"HOME": "/home/u", "XDG_STATE_HOME": "rel"},
			want: Dirs{State: "/home/u/.local/state/mutti", Data: "/home/u/.local/share/mutti", Config: "/home/u/.config/mutti"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromEnv(env(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFromEnvWithoutHome(t *testing.T) {
	if _, err := FromEnv(env(nil)); err == nil {
		t.Fatal("want error without HOME")
	}
}

func TestPaths(t *testing.T) {
	d := Dirs{State: "/s/mutti"}
	if got := d.LockFile("router"); got != "/s/mutti/locks/router.lock" {
		t.Errorf("LockFile = %s", got)
	}
	if got := d.InstanceDir("shop-main"); got != "/s/mutti/instances/shop-main" {
		t.Errorf("InstanceDir = %s", got)
	}
}

func TestFromEnvFailsPerDirWithoutHome(t *testing.T) {
	for _, set := range []map[string]string{
		{"XDG_STATE_HOME": "/s"},
		{"XDG_STATE_HOME": "/s", "XDG_DATA_HOME": "/d"},
	} {
		if _, err := FromEnv(env(set)); err == nil {
			t.Errorf("%v: want error", set)
		}
	}
}
