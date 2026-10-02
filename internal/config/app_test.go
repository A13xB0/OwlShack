package config

import "testing"

func TestValidate_AppPorts(t *testing.T) {
	t.Parallel()
	web := "0.0.0.0:8860"
	app := func(port int, bind string) *AppConnection { return &AppConnection{Port: port, Bind: bind} }
	for _, tt := range []struct {
		name string
		a, b *AppConnection
		ok   bool
	}{
		{"one port", app(5055, ""), nil, true},
		{"two ports", app(5055, ""), app(5056, ""), true},
		{"same port on two addresses", app(5055, "10.0.0.1"), app(5055, "10.0.0.2"), true},
		{"same port twice", app(5055, ""), app(5055, ""), false},
		{"every address clashes with one", app(5055, ""), app(5055, "10.0.0.1"), false},
		{"the web UI's port", app(8860, ""), nil, false},
		{"port 0", app(0, ""), nil, false},
		{"port too high", app(70000, ""), nil, false},
		{"bind not an IP", app(5055, "pole.lab"), nil, false},
	} {
		cfg := Config{ListenAddr: &web, Companions: []CompanionConfig{{Name: "a", App: tt.a}, {Name: "b", App: tt.b}}}
		if err := cfg.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}
