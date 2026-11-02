package sqsmcp

import "testing"

func TestReadCacheEvictsTheOldest(t *testing.T) {
	c := newReadCache(2)
	for _, id := range []string{"a", "b", "c"} {
		c.put(&readMessage{QueueURL: "q", MessageID: id})
	}
	if _, ok := c.get("q", "a"); ok {
		t.Fatal("a should be evicted")
	}
	for _, id := range []string{"b", "c"} {
		if _, ok := c.get("q", id); !ok {
			t.Fatalf("%s missing", id)
		}
	}
}

func TestReadCacheReplacesWithoutGrowing(t *testing.T) {
	c := newReadCache(2)
	for _, id := range []string{"a", "a", "b"} {
		c.put(&readMessage{QueueURL: "q", MessageID: id})
	}
	for _, id := range []string{"a", "b"} {
		if _, ok := c.get("q", id); !ok {
			t.Fatalf("%s missing", id)
		}
	}
}

func TestReadCacheRefreshesAReplacedEntry(t *testing.T) {
	c := newReadCache(2)
	for _, id := range []string{"a", "b", "a", "c"} {
		c.put(&readMessage{QueueURL: "q", MessageID: id})
	}
	if _, ok := c.get("q", "b"); ok {
		t.Fatal("b should be evicted")
	}
	for _, id := range []string{"a", "c"} {
		if _, ok := c.get("q", id); !ok {
			t.Fatalf("%s missing", id)
		}
	}
}

func TestReadCacheRemoveForgetsAMessage(t *testing.T) {
	c := newReadCache(2)
	c.put(&readMessage{QueueURL: "q", MessageID: "a"})
	c.put(&readMessage{QueueURL: "q", MessageID: "b"})
	c.remove("q", "a")
	c.put(&readMessage{QueueURL: "q", MessageID: "c"})
	if _, ok := c.get("q", "a"); ok {
		t.Fatal("a still cached")
	}
	if _, ok := c.get("q", "b"); !ok {
		t.Fatal("b evicted although a slot was free")
	}
}
