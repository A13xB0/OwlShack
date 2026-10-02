package repeater

import (
	"context"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/node/advert"
)

// Firmware setup() holds the boot advert ~16s to let the radio settle.
const bootAdvertDelay = 16 * time.Second

// advertLoop runs the firmware's two schedules, zero-hop and flood; either interval of 0 disables that one.
func (r *Repeater) advertLoop(ctx context.Context) {
	localSecs := r.cfg.AdvertIntervalOr()
	floodSecs := r.cfg.FloodAdvertIntervalOr()

	boot := time.NewTimer(bootAdvertDelay)
	defer boot.Stop()

	// A nil channel blocks forever in select, disabling a schedule whose interval is 0.
	var localC, floodC <-chan time.Time
	if localSecs > 0 {
		t := time.NewTicker(time.Duration(localSecs) * time.Second)
		defer t.Stop()
		localC = t.C
	}
	if floodSecs > 0 {
		t := time.NewTicker(time.Duration(floodSecs) * time.Second)
		defer t.Stop()
		floodC = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-boot.C:
			if err := r.sendAdvert(false); err != nil { // zero-hop, matching firmware boot
				r.log.Error("initial advert error", "error", err)
			}
		case <-localC:
			if err := r.sendAdvert(false); err != nil {
				r.log.Error("zero-hop advert error", "error", err)
			}
		case <-floodC:
			if err := r.sendAdvert(true); err != nil {
				r.log.Error("flood advert error", "error", err)
			}
		}
	}
}

// SendAdvert broadcasts a self-advert on demand; flood=false reaches direct neighbours only.
func (r *Repeater) SendAdvert(flood bool) error { return r.sendAdvert(flood) }

// sendAdvert emits a signed REPEATER advert, scoped through the configured default region when one is set.
func (r *Repeater) sendAdvert(flood bool) error {
	err := advert.SendSelf(r.node, r.log, "REPEATER", r.cfg.Name,
		r.cfg.Latitude, r.cfg.Longitude, flood, r.cfg.PathHashSizeOr(), r.defaultRegionScope())
	if err == nil {
		r.countTx(flood)
	}
	return err
}

// defaultRegionScope resolves the configured scope; nil means an unscoped flood, including when the name is dangling.
func (r *Repeater) defaultRegionScope() *meshcore.Region {
	name := r.cfgSnapshot().DefaultRegion
	if name == "" || name == config.WildcardRegion {
		return nil
	}
	for _, rg := range r.node.Regions().All() {
		if config.SameRegion(rg.Name, name) {
			return rg
		}
	}
	return nil
}
