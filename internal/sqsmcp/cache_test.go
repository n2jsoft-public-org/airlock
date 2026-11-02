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
