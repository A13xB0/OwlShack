package companion

import (
	"context"
	"time"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/node/advert"
)

func (c *Companion) advertLoop(ctx context.Context) {
	err := c.advert()
	if err != nil {
		c.log.Error("initial advert error", "error", err)
	}

	advertInterval := c.cfg.AdvertInterval
	if advertInterval == nil || *advertInterval < 1 {
		oneDay := 86400
		advertInterval = &oneDay
	}

	interval := time.Duration(*advertInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := c.advert()
			if err != nil {
				c.log.Error("advert error", "error", err)
			}
		}
	}
}

func (c *Companion) advert() error {
	return c.sendAdvert(true)
}

// SendAdvert broadcasts a self-advert; flood=false is the firmware's zero-hop advert, seen only by direct neighbours and never rebroadcast.
func (c *Companion) SendAdvert(flood bool) error {
	return c.sendAdvert(flood)
}

func (c *Companion) sendAdvert(flood bool) error {
	return advert.SendSelf(c.node, c.log, "CHAT", c.cfg.Name,
		c.cfg.Latitude, c.cfg.Longitude, flood, int(c.pathHashSize()), c.node.FloodScope())
}

// pathHashSize is the per-hop path hash width in bytes; startup resolves the global default into the block, so nil only happens in tests.
func (c *Companion) pathHashSize() uint8 {
	if c.cfg.PathHashSize == nil {
		return config.DefaultPathHashSize
	}
	return uint8(*c.cfg.PathHashSize)
}
