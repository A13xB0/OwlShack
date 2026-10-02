package companion

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// The observer publishes under its origin's key when it has one, and under the companion's own when it does not.
func TestObserver_PublishesAsItsOrigin(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "origin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mux := node.NewRadioMux(&airModem{})
	defer mux.Stop()

	companionKey, repeaterKey := strings.Repeat("11", 32), strings.Repeat("22", 32)
	repeater, err := config.LocalIdentityFromHex(repeaterKey)
	if err != nil {
		t.Fatal(err)
	}
	statusTopic := func(origin *config.MqttOrigin) string {
		t.Helper()
		iata := "EDI"
		// Nothing listens on port 1: the broker is registered and retries, which is all the topic needs.
		mq := &config.MqttConfig{IataCode: &iata, Origin: origin,
			Brokers: []config.BrokerConfig{{Name: "map", Enabled: true, Host: "127.0.0.1", Port: 1}}}
		c, err := NewCompanion(config.CompanionConfig{Name: "home", PrivateKey: companionKey, Mqtt: mq}, mux, st, nil, nil, nil, new(atomic.Uint64))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Stop()
		if err := c.Observer().Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		sts, ok := c.MqttStatus()
		if !ok || len(sts) != 1 {
			t.Fatalf("observer statuses = %v, %v", sts, ok)
		}
		return sts[0].StatusTopic
	}
	upper := func(id meshcore.LocalIdentity) string {
		pk := id.PublicKey()
		return strings.ToUpper(hex.EncodeToString(pk[:]))
	}

	companion, _ := config.LocalIdentityFromHex(companionKey)
	if got := statusTopic(nil); !strings.Contains(got, upper(companion)) {
		t.Errorf("without an origin the status topic is %q, want the companion's key", got)
	}
	if got := statusTopic(&config.MqttOrigin{Name: "Cadham Village", PrivateKey: repeaterKey}); !strings.Contains(got, upper(repeater)) {
		t.Errorf("with the repeater as origin the status topic is %q, want the repeater's key", got)
	}
	if _, err := NewCompanion(config.CompanionConfig{Name: "home", PrivateKey: companionKey,
		Mqtt: &config.MqttConfig{Origin: &config.MqttOrigin{Name: "bad", PrivateKey: "zz"}}}, mux, st, nil, nil, nil, nil); err == nil {
		t.Error("an origin with a malformed key was accepted")
	}
}
