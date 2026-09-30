package config

import (
	"strings"
	"testing"
)

// openHop firmware runs its serial link at 921600 only; KISS and openHop over the network keep 115200 as the default.
func TestBaudRate_OpenhopSerialIs921600(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		connection string
		want       int
	}{
		{"openhop:///dev/ttyUSB0", 921600},
		{"openhop://192.168.1.5:5055", 115200},
		{"serial:///dev/ttyACM0", 115200},
	} {
		c := &Config{Connection: strPtr(tc.connection)}
		c.ApplyDefaults()
		if *c.BaudRate != tc.want {
			t.Errorf("%s: default baudRate %d, want %d", tc.connection, *c.BaudRate, tc.want)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s: its own default is refused: %v", tc.connection, err)
		}
	}

	slow := 115200
	c := &Config{Connection: strPtr("openhop:///dev/ttyUSB0"), BaudRate: &slow}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "921600") {
		t.Errorf("openHop serial at 115200: %v, want a refusal naming 921600", err)
	}
	c = &Config{Connection: strPtr("openhop://192.168.1.5:5055"), BaudRate: &slow}
	if err := c.Validate(); err != nil {
		t.Errorf("openHop over the network has no baud rate to get wrong: %v", err)
	}
}
