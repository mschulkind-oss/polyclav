package clapcache

import (
	"sort"
	"sync"

	"github.com/mschulkind-oss/polyclav/internal/audio"
)

// Cache stores the host's best active-instance view of CLAP parameters.
// Metadata comes from plugin discovery; values are then updated from local
// writes and plugin feedback so dev tooling does not keep showing launch-time
// defaults after the active instance changes.
type Cache struct {
	mu     sync.RWMutex
	params map[uint32]audio.ClapParamInfo
}

func New() *Cache { return &Cache{params: map[uint32]audio.ClapParamInfo{}} }

func (c *Cache) Replace(params []audio.ClapParamInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.params = make(map[uint32]audio.ClapParamInfo, len(params))
	for _, p := range params {
		c.params[p.ClapID] = p
	}
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.params = map[uint32]audio.ClapParamInfo{}
}

func (c *Cache) Update(id uint32, value float64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.params[id]
	if !ok {
		return false
	}
	p.CurrentValue = value
	c.params[id] = p
	return true
}

func (c *Cache) Get(id uint32) (audio.ClapParamInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.params[id]
	return p, ok
}

func (c *Cache) All() []audio.ClapParamInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]audio.ClapParamInfo, 0, len(c.params))
	for _, p := range c.params {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClapID < out[j].ClapID })
	return out
}
