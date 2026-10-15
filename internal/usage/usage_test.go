package usage

import (
	"context"
	"testing"
	"time"
)

func TestStamp(t *testing.T) {
	c := Stamp(context.Background(), Call{Provider: Meta})
	if c.StoryID != 0 || c.Job != "" || c.At.IsZero() {
		t.Fatalf("outside a job: %+v", c)
	}
	at := time.Unix(100, 0)
	c = Stamp(WithJob(context.Background(), 9, "render"), Call{At: at})
	if c.StoryID != 9 || c.Job != "render" || !c.At.Equal(at) {
		t.Fatalf("inside a job: %+v", c)
	}
}
