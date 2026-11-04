package sqsmcp

import (
	"slices"
	"sync"
)

// readCache keeps the last messages read, so repost_message can resend one without the model retyping it.
type readCache struct {
	items map[string]*readMessage
	order []string
	size  int
	mu    sync.Mutex
}

func newReadCache(size int) *readCache {
	return &readCache{items: map[string]*readMessage{}, size: size}
}

func cacheKey(queueURL, messageID string) string { return queueURL + "\x00" + messageID }

// put stores m, replacing a message with the same key and evicting the oldest one when full.
func (c *readCache) put(m *readMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := cacheKey(m.QueueURL, m.MessageID)
	if _, ok := c.items[k]; ok {
		c.order = slices.DeleteFunc(c.order, func(o string) bool { return o == k })
	} else if len(c.order) >= c.size {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.order = append(c.order, k)
	c.items[k] = m
}

func (c *readCache) get(queueURL, messageID string) (*readMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.items[cacheKey(queueURL, messageID)]
	return m, ok
}

// take gets and forgets a message in one step, so two callers cannot both act on it.
func (c *readCache) take(queueURL, messageID string) (*readMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := cacheKey(queueURL, messageID)
	m, ok := c.items[k]
	if ok {
		delete(c.items, k)
		c.order = slices.DeleteFunc(c.order, func(o string) bool { return o == k })
	}
	return m, ok
}

// remove forgets a message, so it cannot be acted on again.
func (c *readCache) remove(queueURL, messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := cacheKey(queueURL, messageID)
	delete(c.items, k)
	c.order = slices.DeleteFunc(c.order, func(o string) bool { return o == k })
}
