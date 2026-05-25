package report

import "time"

// nowFn is overridable for deterministic tests.
var nowFn = time.Now
