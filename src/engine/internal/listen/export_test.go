package listen

import "time"

// SetBackoff shortens a client's first wait before a re-open, so tests re-open quickly.
func SetBackoff(c *Client, d time.Duration) { c.backoff = d }
