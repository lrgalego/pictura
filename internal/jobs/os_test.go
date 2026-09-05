package jobs

import "os"

func osRemove(p string) error { return os.Remove(p) }
